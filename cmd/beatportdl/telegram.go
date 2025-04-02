package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// InitTelegramBot initializes and starts the Telegram bot.
// It listens for messages, validates them as URLs, and sends valid TelegramRequest structs to the application channel.
func InitTelegramBot(app *application) { // Accept the application instance
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Define a handler function that captures the app instance
	handlerFunc := func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.Text == "" {
			// Ignore updates without messages or text
			return
		}

		messageText := update.Message.Text
		chatID := update.Message.Chat.ID

		// Basic URL validation
		_, err := url.ParseRequestURI(messageText)
		isBeatport := strings.Contains(messageText, "beatport.com")
		isBeatsource := strings.Contains(messageText, "beatsource.com")

		if err == nil && (isBeatport || isBeatsource) {
			// Create and send the TelegramRequest to the main processing loop
			request := TelegramRequest{
				URL:    messageText,
				ChatID: chatID,
			}
			app.telegramRequests <- request // Send the request struct
			// Send confirmation back to user
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, // Use the captured chatID
				Text:   "✅ Received and queued. Processing...",
			})
		} else {
			// Send error message back to user
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "❌ Invalid URL. Please send a valid Beatport or Beatsource URL.",
			})
		}
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(handlerFunc), // Use the handlerFunc with captured channel
	}

	// Replace with your actual bot token (consider using environment variables)
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		fmt.Print("telegram bot token not found: please set TELEGRAM_BOT_TOKEN environment variable")
		return
	}

	b, err := bot.New(botToken, opts...)
	if err != nil {
		// Instead of panic, maybe log and exit gracefully or return an error
		fmt.Printf("Error creating Telegram bot: %v\n", err)
		return // Exit the function if bot creation fails
	}

	app.telegramBot = b // Store the bot instance in the application struct
	app.botCtx = ctx    // Store the context used for the bot

	fmt.Println("Telegram bot starting...")
	b.Start(ctx)                         // Start the bot
	fmt.Println("Telegram bot stopped.") // Will print when context is cancelled
}

// Original handler removed as logic is now inside InitTelegramBot's handlerFunc
