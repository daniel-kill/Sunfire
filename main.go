// Sunfire — Daniel Kill's personal Discord bot (Go / discordgo).
// Primary server: Sunfire (1557930395868209162)
// Bot owner: Daniel Kill (daniel@danielkill.online)
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"sunfire/a2e"
	"sunfire/providers"
	"sunfire/venice"
)

func main() {
	token, err := botToken()
	if err != nil {
		log.Fatalf("failed to get bot token: %v", err)
	}
	if token == "" || token == "PASTE_YOUR_BOT_TOKEN_HERE" {
		log.Fatal("bot token not configured")
	}

	if err := a2e.Init(); err != nil {
		log.Printf("warning: a2e subpackage init: %v", err)
	}
	if err := venice.Init(); err != nil {
		log.Printf("warning: venice subpackage init: %v", err)
	}
	if err := providers.Init(); err != nil {
		log.Printf("warning: providers subpackage init: %v", err)
	}

	// Populate command specs (including /openrouter) before creating the bot.
	buildCommandSpecs()

	bot, err := NewBot(token)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}
	defer bot.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bot.Open(); err != nil {
		log.Fatalf("cannot open session: %v", err)
	}
	<-ctx.Done()
	log.Println("shutdown signal received")
}
