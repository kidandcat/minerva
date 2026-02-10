package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
)

// renderHTMLToImage renders HTML content to a PNG screenshot.
// Returns the path to the saved image file, or an error if Chrome is not available.
func renderHTMLToImage(htmlContent string) (string, error) {
	// Save HTML to a temp file so chromedp can load it
	htmlPath := filepath.Join(os.TempDir(), fmt.Sprintf("email_%d.html", time.Now().UnixNano()))
	if err := os.WriteFile(htmlPath, []byte(htmlContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write HTML file: %w", err)
	}
	defer os.Remove(htmlPath)

	fileURL := "file://" + htmlPath

	// Create headless Chrome context with short timeout
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("disable-dev-shm-usage", true),
		)...,
	)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 30 second timeout for the whole render
	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var buf []byte
	var contentHeight float64

	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(800, 600),
		chromedp.Navigate(fileURL),
		// Wait for page to be ready
		chromedp.WaitReady("body"),
		// Small delay for rendering
		chromedp.Sleep(500*time.Millisecond),
		// Get the full page height
		chromedp.Evaluate(`document.documentElement.scrollHeight`, &contentHeight),
	)
	if err != nil {
		return "", fmt.Errorf("chrome render failed: %w", err)
	}

	// Cap at a reasonable height (max ~5000px)
	height := int64(math.Min(contentHeight, 5000))
	if height < 600 {
		height = 600
	}

	// Take full-page screenshot at the detected height
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(800, height),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.FullScreenshot(&buf, 90),
	)
	if err != nil {
		return "", fmt.Errorf("screenshot failed: %w", err)
	}

	// Save screenshot
	imgPath := filepath.Join(os.TempDir(), fmt.Sprintf("email_%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(imgPath, buf, 0644); err != nil {
		return "", fmt.Errorf("failed to write screenshot: %w", err)
	}

	return imgPath, nil
}
