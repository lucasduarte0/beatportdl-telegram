package main

import (
	"fmt"
	"github.com/fatih/color"
	"github.com/vbauerster/mpb/v8"
	"io"
	"os"
	"sync"
	"unspok3n/beatportdl/config"
	"unspok3n/beatportdl/internal/beatport"
)

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
	telegramURLs     chan string         // Channel to receive URLs from Telegram bot
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
		config:       cfg,
		downloadSem:  make(chan struct{}, cfg.MaxDownloadWorkers), // Channel for download concurrency control
		globalSem:    make(chan struct{}, cfg.MaxGlobalWorkers),   // Channel for global concurrency control
		logWriter:    os.Stdout,                                   // Default to stdout for logging
		telegramURLs: make(chan string, 10),                       // Buffered channel for Telegram URLs
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
	go InitTelegramBot(app.telegramURLs) // Pass the channel

	// Main processing loop - listens for URLs from Telegram
	fmt.Println("Waiting for URLs from Telegram...")
	for url := range app.telegramURLs {
		// Add received URL to the list to be processed
		app.urls = append(app.urls, url)
		fmt.Printf("Received URL from Telegram: %s\n", url) // Log received URL

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

			// Handle the URL in a background goroutine
			app.background(func() {
				app.handleUrl(currentURL)
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

	// This part might not be reached if telegramURLs channel is never closed
	fmt.Println("Telegram URL channel closed. Exiting.")
}
