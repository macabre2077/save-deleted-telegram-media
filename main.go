// deleted_media_backup.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv" // Still needed for conversion
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"github.com/joho/godotenv" // Import godotenv
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore" // Import for log level configuration
)

func main() {
	ctx := context.Background()

	// --- Load Environment Variables ---
	// Load .env file from the current directory.
	// It's often okay if it doesn't exist, environment variables might be set directly.
	err := godotenv.Load()
	if err != nil {
		// Log a warning instead of failing if .env is not found.
		// You might have set the env vars externally (e.g., Docker, system).
		fmt.Printf("Warning: Could not load .env file: %v. Relying on system environment variables.\n", err)
	}

	// --- Configure Logger ---
	logLevel := zapcore.InfoLevel // Default log level
	envLogLevel := os.Getenv("LOG_LEVEL")
	if envLogLevel != "" {
		parsedLevel, err := zapcore.ParseLevel(envLogLevel)
		if err == nil {
			logLevel = parsedLevel
		} else {
			fmt.Printf("Warning: Invalid LOG_LEVEL '%s' in environment, using default 'INFO'. Error: %v\n", envLogLevel, err)
		}
	}

	logCfg := zap.NewDevelopmentConfig()
	logCfg.Level = zap.NewAtomicLevelAt(logLevel)
	log, err := logCfg.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() // nolint:errcheck
	// --- End Logger Configuration ---

	// --- Get Configuration from Environment ---
	apiIDStr := os.Getenv("API_ID")
	if apiIDStr == "" {
		log.Fatal("API_ID not found in environment variables or .env file. Please set it.")
	}
	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		log.Fatal("Invalid API_ID format in environment (must be an integer)", zap.String("value", apiIDStr), zap.Error(err))
	}

	apiHash := os.Getenv("API_HASH")
	if apiHash == "" {
		log.Fatal("API_HASH not found in environment variables or .env file. Please set it.")
	}

	channelIDStr := os.Getenv("CHANNEL_ID")
	if channelIDStr == "" {
		log.Fatal("CHANNEL_ID not found in environment variables or .env file. Please set it.")
	}
	channelID, err := strconv.ParseInt(channelIDStr, 10, 64)
	if err != nil {
		log.Fatal("Invalid CHANNEL_ID format in environment (must be an integer)", zap.String("value", channelIDStr), zap.Error(err))
	}
	// --- End Configuration ---

	// Session file keeps you logged-in between runs.
	session := filepath.Join(os.TempDir(), "tg.session")
	log.Info("Using session file", zap.String("path", session))

	// Build Telegram client using credentials from environment.
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		Logger:         log,
		SessionStorage: &telegram.FileSessionStorage{Path: session},
	})

	// Authentication flow handles authentication process, like prompting for code and 2FA password.
	// Terminal still uses readUserInput for interactive auth steps.
	flow := auth.NewFlow(Terminal{}, auth.SendCodeOptions{})

	if err := client.Run(ctx, func(ctx context.Context) error {
		log.Info("Connecting to Telegram...")
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
		log.Info("Authentication successful.")

		// Use the channel ID from environment
		log.Info("Getting channel information...", zap.Int64("channel_id", channelID))
		list, err := client.API().ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: channelID, AccessHash: 0}})
		if err != nil {
			// Provide more context on potential channel ID issues
			if strings.Contains(err.Error(), "CHANNEL_INVALID") || strings.Contains(err.Error(), "PEER_ID_INVALID") {
				return fmt.Errorf("failed to get channel info (ID: %d). Error: %w. Please ensure the Channel ID in your .env/environment is correct and the bot/user is a member (or admin) of the channel", channelID, err)
			}
			return fmt.Errorf("ChannelsGetChannels request failed (ID: %d): %w", channelID, err)
		}

		if len(list.GetChats()) == 0 {
			return fmt.Errorf("no channel found for the provided ID: %d (from .env/environment). Ensure the ID is correct and your account is a member", channelID)
		}

		channelInfo, ok := list.GetChats()[0].(*tg.Channel)
		if !ok {
			// Could be a tg.Chat, tg.ChatForbidden etc.
			chat := list.GetChats()[0]
			chatType := fmt.Sprintf("%T", chat) // Get the type
			if forbidden, isForbidden := chat.(*tg.ChannelForbidden); isForbidden {
				return fmt.Errorf("access to channel %d is forbidden until %s. Reason: %s", forbidden.ID, time.Unix(int64(forbidden.UntilDate), 0).Format(time.RFC3339), forbidden.Title)
			}
			return fmt.Errorf("the provided ID %d does not belong to an accessible Channel (supergroup/channel). Found type: %s", channelID, chatType)
		}

		log.Info("Successfully found channel", zap.String("title", channelInfo.Title), zap.Int64("id", channelInfo.ID), zap.Int64("access_hash", channelInfo.AccessHash))

		// Prepare downloader once.
		dl := downloader.NewDownloader()

		// Iterate over log in 100-event pages.
		var (
			maxID int64 // start from 0 = newest
			total int   // Keep track of saved files
		)
		log.Info("Fetching admin log for deleted messages...")
		for {
			req := &tg.ChannelsGetAdminLogRequest{
				Channel: &tg.InputChannel{
					ChannelID:  channelID,
					AccessHash: channelInfo.AccessHash,
				},
				EventsFilter: tg.ChannelAdminLogEventsFilter{
					Delete: true,
				},
				Limit: 100,
				MaxID: maxID,
			}

			log.Debug("Requesting admin log page", zap.Int64("max_id", maxID))
			res, err := client.API().ChannelsGetAdminLog(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to execute GetAdminLog request: %w", err)
			}

			log.Debug("Fetched admin log page", zap.Int("event_count", len(res.Events)), zap.Int("user_count", len(res.Users)), zap.Int("chat_count", len(res.Chats)))

			if len(res.Events) == 0 {
				log.Info("Reached end of admin log for delete events.")
				break // no more pages
			}

			foundDeletedMedia := false
			for _, ev := range res.Events {
				maxID = ev.ID // Update maxID for the next page request (important to do for every event)
				// We only care about delete-message events.
				if del, ok := ev.Action.(*tg.ChannelAdminLogEventActionDeleteMessage); ok {
					// del.Message can be *tg.Message, *tg.MessageService…
					if msg, ok := del.Message.(*tg.Message); ok && msg.Media != nil {
						foundDeletedMedia = true
						log.Info("Found deleted message with media", zap.Int("msg_id", msg.ID), zap.Time("date", time.Unix(int64(msg.Date), 0)))
						if err := saveMedia(ctx, client, dl, msg, log); err != nil {
							// Log warning but continue processing other messages
							log.Warn("Failed to save media", zap.Int("msg_id", msg.ID), zap.Error(err))
						} else if err == nil { // Explicitly check for nil error if saveMedia returns nil for skipped unsupported media
							total++ // Increment counter only on successful save (or skipped)
							log.Info("Successfully processed/saved media for message", zap.Int("msg_id", msg.ID))
						}
					} else if msgService, ok := del.Message.(*tg.MessageService); ok {
						log.Debug("Found deleted service message", zap.Int("msg_id", msgService.ID))
					}
				} else {
					log.Debug("Skipping non-delete admin log event", zap.String("type", fmt.Sprintf("%T", ev.Action)))
				}
			}

			if !foundDeletedMedia && len(res.Events) > 0 {
				log.Debug("Processed admin log batch, but no deleted messages *with media* were found in this batch.")
			}

			// Optional: Add a small delay to avoid hitting rate limits, although GetAdminLog is usually less sensitive.
			// log.Debug("Sleeping briefly before next request...")
			// time.Sleep(500 * time.Millisecond)
		}

		log.Info("Finished processing admin log.", zap.Int("total_files_potentially_saved", total))
		return nil
	}); err != nil {
		// Use Fatal to exit after logging the error
		log.Fatal("Application run failed", zap.Error(err))
	}
}

// saveMedia downloads media contained in msg and stores it in ./media_backup/.
// Added logger as argument for more contextual logging.
func saveMedia(ctx context.Context, client *telegram.Client, dl *downloader.Downloader, msg *tg.Message, log *zap.Logger) error {
	loc, filename, err := inputLocation(msg, log) // Pass logger
	if err != nil {
		// Check if it's the specific "unsupported media" error
		if errors.Is(err, errUnsupportedMedia) {
			log.Debug("Skipping unsupported media type", zap.Int("msg_id", msg.ID))
			return nil // Return nil to indicate it was handled (skipped), not an error
		}
		// Log other input location errors as warnings, allows processing to continue
		log.Warn("Could not get input location", zap.Int("msg_id", msg.ID), zap.Error(err))
		return err // Return the error to be logged by the caller as a failure
	}

	mediaDir := "media_backup"
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return fmt.Errorf("failed to create base directory %s: %w", mediaDir, err)
	}
	if filename == "" {
		filename = fmt.Sprintf("%d_%d.dat", msg.ID, time.Now().UnixNano()) // Add timestamp to fallback filename for uniqueness
		log.Warn("Generated fallback filename", zap.String("filename", filename), zap.Int("msg_id", msg.ID))
	}

	messageTime := time.Unix(int64(msg.Date), 0)
	timestamp := messageTime.Format("20060102_150405")        // YYYYMMDD_HHMMSS
	baseFilename := fmt.Sprintf("%s_%s", timestamp, filename) // Use sanitized filename

	// Determine subdirectory based on FromID (if available)
	var subDirName string
	if from, ok := msg.FromID.(*tg.PeerUser); ok {
		subDirName = strconv.FormatInt(from.UserID, 10)
	} else if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok { // Fallback to PeerID if FromID is nil (e.g., legacy?)
		subDirName = strconv.FormatInt(peerUser.UserID, 10)
	} else if peerChat, ok := msg.PeerID.(*tg.PeerChat); ok {
		subDirName = fmt.Sprintf("chat_%d", peerChat.ChatID)
	} else if peerChannel, ok := msg.PeerID.(*tg.PeerChannel); ok {
		subDirName = fmt.Sprintf("channel_%d", peerChannel.ChannelID)
	} else {
		subDirName = "unknown_sender" // Fallback if no ID available
		log.Warn("Could not determine sender/peer ID for subdirectory", zap.Int("msg_id", msg.ID))
	}
	subDir := filepath.Join(mediaDir, subDirName)

	// Ensure the subdirectory exists.
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return fmt.Errorf("failed to create subdirectory %s: %w", subDir, err)
	}

	// Final destination path inside the subdirectory.
	destPath := filepath.Join(subDir, baseFilename)

	// Check if file already exists to avoid redownloading (optional but good)
	if _, err := os.Stat(destPath); err == nil {
		log.Info("File already exists, skipping download.", zap.String("path", destPath), zap.Int("msg_id", msg.ID))
		return nil // Not an error, just skip
	} else if !os.IsNotExist(err) {
		// Log other stat errors but proceed with download attempt
		log.Warn("Error checking if file exists", zap.String("path", destPath), zap.Error(err))
	}

	// Temporary path *could* be used, but downloading directly might be simpler if rename isn't needed often.
	// Let's download directly to destPath for simplicity now. Add temp path later if needed.
	log.Info("Attempting to download media", zap.String("filename", baseFilename), zap.Int("msg_id", msg.ID), zap.String("destination", destPath))

	// Download directly to the final destination.
	_, err = dl.Download(client.API(), loc).ToPath(ctx, destPath)
	if err != nil {
		// Attempt to remove partially downloaded file on error
		_ = os.Remove(destPath)

		// Generic download error
		return fmt.Errorf("download failed for %s (msg %d): %w", baseFilename, msg.ID, err)
	}

	log.Info("Download successful", zap.String("path", destPath))
	return nil // Explicitly return nil on success
}

// Define a specific error for unsupported media types
var errUnsupportedMedia = errors.New("unsupported media type")

// inputLocation extracts an InputFileLocation + file name from message media.
// Added logger for context.
func inputLocation(msg *tg.Message, log *zap.Logger) (tg.InputFileLocationClass, string, error) {
	switch m := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.AsNotEmpty()
		if !ok {
			details := "nil or empty"
			if _, isEmpty := m.Document.(*tg.DocumentEmpty); isEmpty {
				details = "tg.DocumentEmpty"
			}
			return nil, "", fmt.Errorf("document is %s for msg %d", details, msg.ID)
		}

		name := filenameFromDocument(doc, log) // Pass logger
		if len(doc.FileReference) == 0 {
			log.Warn("Document file reference is empty, download may fail", zap.Int64("doc_id", doc.ID), zap.Int("msg_id", msg.ID))
			// Proceeding anyway, sometimes it works without it on older DCs/files.
		}
		return &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     "", // Not used for documents
		}, name, nil

	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.AsNotEmpty()
		if !ok {
			details := "nil or empty"
			if _, isEmpty := m.Photo.(*tg.PhotoEmpty); isEmpty {
				details = "tg.PhotoEmpty"
			}
			return nil, "", fmt.Errorf("photo is %s for msg %d", details, msg.ID)
		}

		// Find the largest photosize
		var (
			biggest     *tg.PhotoSize // Representative *tg.PhotoSize needed for location
			biggestType string        // The actual type string ('y', 'w', etc.)
			largestDim  int           // Largest dimension (width or height) for comparison
		)

		for _, s := range photo.Sizes {
			currentDim := 0
			currentType := ""
			var currentSize *tg.PhotoSize // Temp holder for *tg.PhotoSize representation

			switch size := s.(type) {
			case *tg.PhotoSize:
				currentDim = max(size.W, size.H)
				currentType = size.Type
				currentSize = size
			case *tg.PhotoSizeProgressive:
				currentDim = max(size.W, size.H) // Use W/H from progressive meta
				currentType = size.Type          // Use the type letter from progressive meta
				// Create a representative *tg.PhotoSize for location API
				currentSize = &tg.PhotoSize{Type: size.Type, W: size.W, H: size.H, Size: -1} // Size_ might not be accurate here
			case *tg.PhotoCachedSize: // Cached sizes usually aren't downloadable directly this way
				log.Debug("Skipping PhotoCachedSize", zap.String("type", size.Type), zap.Int("msg_id", msg.ID))
				continue
			case *tg.PhotoStrippedSize: // Stripped sizes are low-quality previews
				log.Debug("Skipping PhotoStrippedSize", zap.String("type", size.Type), zap.Int("msg_id", msg.ID))
				continue
			default:
				log.Warn("Unknown photo size type", zap.String("type", fmt.Sprintf("%T", s)), zap.Int("msg_id", msg.ID))
				continue
			}

			if currentDim > largestDim {
				largestDim = currentDim
				biggestType = currentType
				biggest = currentSize // Store the *tg.PhotoSize representation
			}
		}

		if biggest == nil || biggestType == "" {
			return nil, "", fmt.Errorf("no suitable photo sizes found for msg %d (photo_id %d)", msg.ID, photo.ID)
		}
		log.Debug("Selected largest photo size", zap.String("type", biggestType), zap.Int("width", biggest.W), zap.Int("height", biggest.H), zap.Int64("photo_id", photo.ID))

		if len(photo.FileReference) == 0 {
			log.Warn("Photo file reference is empty, download may fail", zap.Int64("photo_id", photo.ID), zap.Int("msg_id", msg.ID))
		}

		// Generate filename using photo ID and selected type
		filename := fmt.Sprintf("%d_%s.jpg", photo.ID, biggestType)

		return &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     biggestType, // Use the determined largest type string
		}, filename, nil

	default:
		log.Debug("Unsupported media type in message", zap.Int("msg_id", msg.ID), zap.String("type", fmt.Sprintf("%T", m)))
		return nil, "", errUnsupportedMedia // Use the specific error type
	}
}

// filenameFromDocument tries to extract the original file name. Added logger.
func filenameFromDocument(doc *tg.Document, log *zap.Logger) string {
	for _, a := range doc.Attributes {
		if attr, ok := a.(*tg.DocumentAttributeFilename); ok && attr.FileName != "" {
			log.Debug("Found filename attribute", zap.Int64("doc_id", doc.ID), zap.String("filename", attr.FileName))
			return sanitize(attr.FileName) // Sanitize before returning
		}
	}

	// Fallback if no filename attribute
	ext := "bin" // Default extension
	mimeParts := strings.Split(doc.MimeType, "/")
	if len(mimeParts) == 2 && mimeParts[1] != "" {
		cleanedPart := strings.ToLower(mimeParts[1])
		// Remove potential parameters like in "text/plain; charset=utf-8"
		if idx := strings.Index(cleanedPart, ";"); idx != -1 {
			cleanedPart = cleanedPart[:idx]
		}
		cleanedPart = strings.TrimSpace(cleanedPart)
		// Basic validation for common extensions
		if len(cleanedPart) > 0 && len(cleanedPart) < 10 && !strings.ContainsAny(cleanedPart, `/\:*?"<>| `) {
			ext = cleanedPart
		}
	}
	name := fmt.Sprintf("%d.%s", doc.ID, ext)
	log.Debug("Document has no filename attribute, generated name from MIME", zap.Int64("doc_id", doc.ID), zap.String("mime_type", doc.MimeType), zap.String("generated_name", name))
	return sanitize(name) // Sanitize the generated name too
}

// sanitize removes or replaces characters potentially unsafe for filenames.
func sanitize(s string) string {
	if s == "" {
		return "empty_filename"
	}
	// Replace common path separators first
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")

	// Define a set of characters generally considered unsafe or problematic.
	replacer := strings.NewReplacer(
		":", "_", "*", "_", "?", "_", "\"", "'", "<", "_", ">", "_", "|", "_",
		"\x00", "", // Remove null bytes
		// Remove control characters (0x00-0x1F and 0x7F)
		"\x01", "", "\x02", "", "\x03", "", "\x04", "", "\x05", "", "\x06", "", "\x07", "",
		"\x08", "", "\x09", "", "\x0A", "", "\x0B", "", "\x0C", "", "\x0D", "", "\x0E", "", "\x0F", "",
		"\x10", "", "\x11", "", "\x12", "", "\x13", "", "\x14", "", "\x15", "", "\x16", "", "\x17", "",
		"\x18", "", "\x19", "", "\x1A", "", "\x1B", "", "\x1C", "", "\x1D", "", "\x1E", "", "\x1F", "",
		"\x7F", "",
	)
	sanitized := replacer.Replace(s)

	// Trim leading/trailing spaces and dots (problematic on Windows).
	sanitized = strings.TrimSpace(sanitized)
	sanitized = strings.Trim(sanitized, ".")

	// Prevent overly long filenames (most filesystems have limits around 255 bytes/chars)
	const maxLen = 220 // Leave some room for timestamp/extension etc.
	if len(sanitized) > maxLen {
		// Try to preserve extension if possible
		ext := filepath.Ext(sanitized)
		base := sanitized[:len(sanitized)-len(ext)]
		if len(base) > maxLen-len(ext) {
			base = base[:maxLen-len(ext)]
		}
		sanitized = base + ext
		// If somehow still too long (e.g., no extension), just truncate
		if len(sanitized) > maxLen {
			sanitized = sanitized[:maxLen]
		}
	}

	// Ensure filename is not empty after sanitization
	if sanitized == "" || sanitized == "." || sanitized == ".." {
		// If sanitization resulted in empty or invalid name, provide a default
		return fmt.Sprintf("sanitized_file_%d", time.Now().UnixNano())
	}

	// Windows reserved names check (case-insensitive)
	reservedNames := []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}
	upperSanitized := strings.ToUpper(sanitized)
	// Check base name without extension
	baseName := upperSanitized
	if ext := filepath.Ext(upperSanitized); ext != "" {
		baseName = upperSanitized[:len(upperSanitized)-len(ext)]
	}
	for _, reserved := range reservedNames {
		if baseName == reserved {
			sanitized = "_" + sanitized // Prepend underscore if reserved
			break
		}
	}

	return sanitized
}

// Helper for max calculation needed in photo size logic
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
