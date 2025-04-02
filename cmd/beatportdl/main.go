package main

import (
	"context" // Add context import
	"fmt"
	"io"
	"os"
	"sync"
	"unspok3n/beatportdl/config"
	"unspok3n/beatportdl/internal/beatport"

	"path/filepath" // Add filepath import

	"github.com/fatih/color"
	"github.com/go-telegram/bot"               // Add bot import
	"github.com/go-telegram/bot/models" // Add models import
	"github.com/vbauerster/mpb/v8"
)

// TelegramRequest holds the URL and the ChatID from a Telegram message
type TelegramRequest struct {
	URL    string
	ChatID int64
}

// Constants for configuration and cache filenames
const (
	configFilename = "beatportdl-config.yml"
	cacheFilename  = "beatportdl-credentials.json"
)

// Application struct holds all the state for the program
type application struct {
	config           *config.AppConfig   // Configuration settings
	logFile          *os.File            // File handle for error logging
	logWriter        io.Writer           // Writer for logging (stdout or file)
	bp               *beatport.Beatport  // Beatport API client
	bs               *beatport.Beatport  // Beatsource API client
	wg               sync.WaitGroup      // WaitGroup for tracking concurrent operations
	downloadSem      chan struct{}       // Semaphore for limiting concurrent downloads
	globalSem        chan struct{}       // Semaphore for limiting global concurrency
	pbp              *mpb.Progress       // Progress bar manager
	urls             []string            // URLs to process
	activeFiles      map[string]struct{} // Track active downloads to prevent duplicates
	activeFilesMutex sync.RWMutex        // Mutex for thread-safe access to activeFiles
	telegramRequests chan TelegramRequest  // Channel to receive requests from Telegram bot
	telegramBot      *bot.Bot            // Telegram bot instance
	botCtx           context.Context     // Context for bot operations
}

func main() {
	// Initialize configuration and get paths
	cfg, cachePath, err := Setup()
	if err != nil {
		fmt.Println(err.Error())
		Pause() // Wait for user input before exiting
	}

	// Create application instance with initial configuration
	app := &application{
		config:           cfg,
		downloadSem:      make(chan struct{}, cfg.MaxDownloadWorkers), // Channel for download concurrency control
		globalSem:        make(chan struct{}, cfg.MaxGlobalWorkers),   // Channel for global concurrency control
		logWriter:        os.Stdout,                                   // Default to stdout for logging
		telegramRequests: make(chan TelegramRequest, 10),              // Buffered channel for Telegram requests
		botCtx:           context.Background(),                        // Initialize bot context
	}

	// Set up error logging if enabled in config
	if cfg.WriteErrorLog {
		logFilePath, err := ExecutableDirFilePath("error.log")
		if err != nil {
			fmt.Println(err.Error())
			Pause()
		}
		f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
		if err != nil {
			panic(err)
		}
		app.logFile = f
		defer f.Close() // Ensure file is closed when program exits
	}

	// Initialize authentication for Beatport and Beatsource
	auth := beatport.NewAuth(cfg.Username, cfg.Password, cachePath)
	bp := beatport.New(beatport.StoreBeatport, cfg.Proxy, auth)
	bs := beatport.New(beatport.StoreBeatsource, cfg.Proxy, auth)

	// Try to load cached credentials, or authenticate if needed
	if err := auth.LoadCache(); err != nil {
		if err := auth.Init(bp); err != nil {
			app.FatalError("beatport", err)
		}
	}

	// Store API clients in application
	app.bp = bp
	app.bs = bs

	// Init telegram bot in a separate goroutine
	fmt.Println("Initializing Telegram bot...")
	go InitTelegramBot(app) // Pass the application instance

	// Main processing loop - listens for requests from Telegram
	fmt.Println("Waiting for requests from Telegram...")
	for req := range app.telegramRequests { // Receive TelegramRequest
		// Add received URL to the list to be processed (maybe remove this later if only processing one at a time)
		app.urls = append(app.urls, req.URL)
		fmt.Printf("Received request from Telegram ChatID %d: %s\n", req.ChatID, req.URL) // Log received request

		// --- Batch Processing Logic ---
		// Decide when to trigger processing. For simplicity, let's process immediately.
		// In a real scenario, you might batch them or use a timer.

		// Set up progress bars for this batch (just one URL for now)
		app.pbp = mpb.New(mpb.WithAutoRefresh(), mpb.WithOutput(color.Output))
		app.logWriter = app.pbp
		app.activeFiles = make(map[string]struct{}, len(app.urls)) // Reset active files for this batch

		// Process the current batch of URLs (just the one received for now)
		for _, currentURL := range app.urls {
			// Check if file is already being processed in this batch run
			app.activeFilesMutex.Lock()
			if _, exists := app.activeFiles[currentURL]; exists {
				app.activeFilesMutex.Unlock()
				fmt.Printf("Skipping duplicate URL in current batch: %s\n", currentURL) // Use fmt.Printf
				continue
			}
			app.activeFiles[currentURL] = struct{}{}
			app.activeFilesMutex.Unlock()

			// Handle the URL in a background goroutine, passing the ChatID
			app.background(func() {
				app.handleUrl(currentURL, req.ChatID) // Pass ChatID
			})
		}

		// Wait for all goroutines in this batch to complete
		app.wg.Wait()
		app.pbp.Shutdown() // Clean up progress bars for this batch

		// Reset URLs after processing the batch
		app.urls = []string{}
		fmt.Println("Finished processing batch. Waiting for next URL from Telegram...")
		// --- End Batch Processing Logic ---
	}

	// This part might not be reached if telegramRequests channel is never closed
	fmt.Println("Telegram request channel closed. Exiting.")
}

// sendTrackViaTelegram sends the specified file as a document via Telegram.
func (app *application) sendTrackViaTelegram(chatID int64, filePath string) error {
	if app.telegramBot == nil {
		return fmt.Errorf("telegram bot not initialized")
	}

	// Prepare the document input
	fileContent, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open temporary file %s: %w", filePath, err)
	}
	defer fileContent.Close()

	doc := &models.InputFileUpload{
		Filename: filepath.Base(filePath), // Use the base filename
		Data:     fileContent,
	}

	// Send the document
	_, err = app.telegramBot.SendDocument(app.botCtx, &bot.SendDocumentParams{
		ChatID:  chatID,
		Document: doc,
		// Caption: fmt.Sprintf("Downloaded: %s", filepath.Base(filePath)), // Optional caption
	})

	if err != nil {
		return fmt.Errorf("failed to send document via Telegram: %w", err)
	}

	fmt.Printf("Successfully sent %s to ChatID %d\n", filepath.Base(filePath), chatID)
	return nil
}
