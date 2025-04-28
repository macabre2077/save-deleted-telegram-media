# Telegram Deleted Media Backup Tool

This Go script connects to Telegram using your API credentials, scans the admin log of a specified channel (where you have admin rights) for message deletion events, and attempts to download the media (photos, documents) from those deleted messages.

## Features

*   Scans a specific Telegram channel's admin log for message deletion events.
*   Attempts to download associated media (photos, documents) from deleted messages.
*   Requires channel admin rights with the "View Admin Log" permission.
*   Uses a `.env` file for secure configuration of API credentials and channel ID.
*   Organizes downloaded files into a `media_backup` directory, sorted into subdirectories by the sender's user ID.
*   Handles interactive Telegram login (phone code, 2FA password) on the first run or when the session expires.
*   Creates a session file (`tg.session` in the system's temporary directory) to stay logged in between runs.
*   Configurable logging level via `.env` file.

## Prerequisites

*   **Go:** Version 1.18 or later installed.
*   **Telegram Account:** An active Telegram account.
*   **Telegram API Credentials:**
    *   `API_ID`
    *   `API_HASH`
    *   You can obtain these from Telegram's official site: [https://my.telegram.org/apps](https://my.telegram.org/apps)
*   **Channel Admin Rights:** You must be an administrator in the target channel with the **"View Admin Log"** permission enabled for your account.

## Configuration (`.env` File)

Create a `.env` file in the script's directory with the following content, replacing the placeholder values with your own:

```dotenv
# .env file

# Telegram API Credentials - Get from https://my.telegram.org/apps
# NEVER commit this file to version control if it contains real credentials!
API_ID=12345678
API_HASH=your_api_hash_string_here

# Target Channel ID (numeric ID only, without the -100 prefix)
# Find this by forwarding a message from the channel to @RawDataBot or similar bots.
# Or use https://api.telegram.org/bot{botToken}/getChat?chat_id=@chatSlug to retrieve.
CHANNEL_ID=1234567890

# Optional: Set log level (DEBUG, INFO, WARN, ERROR) - Defaults to INFO if not set
# LOG_LEVEL=DEBUG
```

*   **`API_ID` / `API_HASH`:** Your unique developer credentials from Telegram.
*   **`CHANNEL_ID`:** The numeric ID of the channel you want to scan. **Do not include** the `-100` prefix commonly seen for channel IDs. You often need to use a bot like `@RawDataBot` or inspect web links to find the correct numeric ID.
*   **`LOG_LEVEL` (Optional):** Controls the verbosity of the log output. `DEBUG` is useful for troubleshooting. Defaults to `INFO`.

## Usage

1.  **First Run / Authentication:** The script will prompt you in the terminal for:
    *   Your phone number (associated with your Telegram account).
    *   The confirmation code sent to your Telegram account.
    *   Your 2FA password, if you have one enabled.
        A session file (`tg.session`) will be created in your system's temporary directory to keep you logged in for subsequent runs.

2.  **Processing:** The script will connect, fetch the channel info, and then start iterating through the admin log pages looking for deleted messages with media. Downloads will be logged, and any errors encountered during download will be reported.

## Output

Downloaded media files will be saved in a directory named `media_backup` created in the same location where you run the script.

Inside `media_backup`, files are organized into subdirectories named after the **User ID** of the person who originally sent the message (if available):

```
media_backup/
├── 111111111/          # Files from User ID 111111111
│   └── 20231027_103015_document_12345.pdf
│   └── 20231027_110500_photo_67890_y.jpg
├── 222222222/          # Files from User ID 222222222
│   └── 20231026_150000_video_abcde.mp4
```

Filenames are structured as `YYYYMMDD_HHMMSS_<original_or_generated_filename>`.
## Dependencies

*   [github.com/gotd/td](https://github.com/gotd/td): Telegram MTProto library.
*   [github.com/joho/godotenv](https://github.com/joho/godotenv): Loading environment variables from `.env` files.
*   [go.uber.org/zap](https://github.com/uber-go/zap): Fast, structured logging.
