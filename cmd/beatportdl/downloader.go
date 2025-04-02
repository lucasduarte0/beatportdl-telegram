package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lucasduarte0/beatportdl-telegram/config"
	"lucasduarte0/beatportdl-telegram/internal/beatport"
	"sync"

	"github.com/go-telegram/bot" // Add models import
	"github.com/google/uuid"
)

func (app *application) errorLogWrapper(url, step string, err error) {
	app.LogError(fmt.Sprintf("[%s] %s", url, step), err)
}

func (app *application) infoLogWrapper(url, message string) {
	app.LogInfo(fmt.Sprintf("[%s] %s", url, message))
}

func (app *application) createDirectory(baseDir string, subDir ...string) (string, error) {
	fullPath := filepath.Join(baseDir, filepath.Join(subDir...))
	err := CreateDirectory(fullPath)
	return fullPath, err
}

type DownloadsDirectoryEntity interface {
	DirectoryName(n beatport.NamingPreferences) string
}

func (app *application) setupDownloadsDirectory(baseDir string, entity DownloadsDirectoryEntity) (string, error) {
	if app.config.SortByContext {
		var subDir string
		switch castedEntity := entity.(type) {
		case *beatport.Release:
			subDir = castedEntity.DirectoryName(
				beatport.NamingPreferences{
					Template:           app.config.ReleaseDirectoryTemplate,
					Whitespace:         app.config.WhitespaceCharacter,
					ArtistsLimit:       app.config.ArtistsLimit,
					ArtistsShortForm:   app.config.ArtistsShortForm,
					TrackNumberPadding: app.config.TrackNumberPadding,
				},
			)
			if app.config.SortByLabel && entity != nil {
				baseDir = filepath.Join(baseDir, castedEntity.Label.Name)
			}
		case *beatport.Playlist:
			subDir = castedEntity.DirectoryName(
				beatport.NamingPreferences{
					Template:           app.config.PlaylistDirectoryTemplate,
					Whitespace:         app.config.WhitespaceCharacter,
					TrackNumberPadding: app.config.TrackNumberPadding,
				},
			)
		case *beatport.Chart:
			subDir = castedEntity.DirectoryName(
				beatport.NamingPreferences{
					Template:           app.config.ChartDirectoryTemplate,
					Whitespace:         app.config.WhitespaceCharacter,
					TrackNumberPadding: app.config.TrackNumberPadding,
				},
			)
		case *beatport.Label:
			subDir = castedEntity.DirectoryName(
				beatport.NamingPreferences{
					Template:   app.config.LabelDirectoryTemplate,
					Whitespace: app.config.WhitespaceCharacter,
				},
			)
		case *beatport.Artist:
			subDir = castedEntity.DirectoryName(
				beatport.NamingPreferences{
					Template:   app.config.ArtistDirectoryTemplate,
					Whitespace: app.config.WhitespaceCharacter,
				},
			)
		}
		baseDir = filepath.Join(baseDir, subDir)
	}
	return app.createDirectory(baseDir)
}

func (app *application) requireCover(respectFixTags, respectKeepCover bool) bool {
	fixTags := respectFixTags && app.config.FixTags &&
		(app.config.CoverSize != config.DefaultCoverSize || app.config.Quality != "lossless")
	keepCover := respectKeepCover && app.config.SortByContext && app.config.KeepCover
	return fixTags || keepCover
}

func (app *application) downloadCover(image beatport.Image, downloadsDir string) (string, error) {
	coverUrl := image.FormattedUrl(app.config.CoverSize)
	coverPath := filepath.Join(downloadsDir, uuid.New().String())
	err := app.downloadFile(coverUrl, coverPath, "")
	if err != nil {
		os.Remove(coverPath)
		return "", err
	}
	return coverPath, nil
}

func (app *application) handleCoverFile(path string) error {
	if path == "" {
		return nil
	}
	if app.config.KeepCover && app.config.SortByContext {
		newPath := filepath.Dir(path) + "/cover.jpg"
		if err := os.Rename(path, newPath); err != nil {
			return err
		}
	} else {
		os.Remove(path)
	}
	return nil
}

var (
	ErrTrackFileExists    = errors.New("file already exists")
	ErrTelegramBotMissing = errors.New("telegram bot instance is missing in application context") // Add new error
)

// saveTrack downloads the track to a temporary location and returns the path.
func (app *application) saveTrack(inst *beatport.Beatport, track *beatport.Track, quality string) (tempFilePath string, err error) {
	var fileExtension string
	var displayQuality string

	var stream *beatport.TrackStream
	var download *beatport.TrackDownload

	switch app.config.Quality {
	case "medium-hls":
		trackStream, err := inst.StreamTrack(track.ID)
		if err != nil {
			return "", err
		}
		fileExtension = ".m4a"
		displayQuality = "AAC 128kbps - HLS"
		stream = trackStream
	default:
		trackDownload, err := inst.DownloadTrack(track.ID, quality)
		if err != nil {
			return "", err
		}
		switch trackDownload.StreamQuality {
		case ".128k.aac.mp4":
			fileExtension = ".m4a"
			displayQuality = "AAC 128kbps"
		case ".256k.aac.mp4":
			fileExtension = ".m4a"
			displayQuality = "AAC 256kbps"
		case ".flac":
			fileExtension = ".flac"
			displayQuality = "FLAC"
		default:
			return "", fmt.Errorf("invalid stream quality: %s", trackDownload.StreamQuality)
		}
		download = trackDownload
	}

	// Generate a base filename (without directory)
	baseFileName := track.Filename(
		beatport.NamingPreferences{
			Template:           app.config.TrackFileTemplate, // Use configured template for filename part
			Whitespace:         app.config.WhitespaceCharacter,
			ArtistsLimit:       app.config.ArtistsLimit,
			ArtistsShortForm:   app.config.ArtistsShortForm,
			TrackNumberPadding: app.config.TrackNumberPadding,
			KeySystem:          app.config.KeySystem,
		},
	) + fileExtension // Add the determined extension

	// Create a temporary file path
	tempDir := os.TempDir()
	// Use a more robust way to create a unique temp file name if needed, but UUID should suffice for now
	tempFilePath = filepath.Join(tempDir, SanitizeFilename(baseFileName)) // Sanitize baseFileName

	// Note: Skipping existing file check as it's a unique temp file.
	// If you need duplicate *download* prevention across runs, more state is needed.

	// Lock active files based on the temp file path during download
	app.activeFilesMutex.Lock()
	app.activeFiles[tempFilePath] = struct{}{} // Track the temp file path
	app.activeFilesMutex.Unlock()

	var prefix string
	infoDisplay := fmt.Sprintf("%s (%s) [%s]", track.Name.String(), track.MixName.String(), displayQuality)
	if app.config.ShowProgress {
		prefix = infoDisplay
	} else {
		fmt.Println("Downloading " + infoDisplay)
	}

	// Download to the temporary file path
	if download != nil {
		if err = app.downloadFile(download.Location, tempFilePath, prefix); err != nil {
			os.Remove(tempFilePath) // Clean up failed download
			return "", err
		}
	} else if stream != nil {
		segments, key, errStream := getStreamSegments(stream.Url)
		if errStream != nil {
			return "", fmt.Errorf("get stream segments: %v", errStream)
		}
		// Use temp dir for segment downloads as well
		segmentsFile, errSegments := app.downloadSegments(tempDir, *segments, *key, prefix)
		if errSegments != nil {
			os.Remove(tempFilePath) // Clean up potentially partially created file
			return "", fmt.Errorf("download segments: %v", errSegments)
		}
		defer os.Remove(segmentsFile) // Clean up concatenated segments file

		if errRemux := remuxToM4A(segmentsFile, tempFilePath); errRemux != nil {
			os.Remove(tempFilePath) // Clean up failed remux
			return "", fmt.Errorf("remux to m4a: %v", errRemux)
		}
	} else {
		// Should not happen if logic above is correct
		return "", errors.New("no download or stream source found")
	}

	if !app.config.ShowProgress {
		fmt.Printf("Finished downloading %s to %s\n", infoDisplay, tempFilePath)
	}

	// Return the path to the temporary file
	return tempFilePath, nil
}

const (
	rawTagSuffix = "_raw"
)

// handleTrack downloads, tags, sends via Telegram, and cleans up a single track.
func (app *application) handleTrack(inst *beatport.Beatport, track *beatport.Track, coverPath string, chatID int64) error { // Add chatID parameter
	// 1. Save track to temporary location
	//    Call the updated saveTrack (inst, track, quality)
	tempLocation, err := app.saveTrack(inst, track, app.config.Quality)
	if err != nil {
		// If saveTrack returns nil error but empty path (e.g., skipped existing), handle it
		if tempLocation == "" && err == nil {
			app.infoLogWrapper(track.StoreUrl(), "Track skipped or already handled.")
			return nil // Not an error, just skipped
		}
		return fmt.Errorf("save track to temp: %w", err)
	}
	if tempLocation == "" { // Should ideally be caught by err check above, but double-check
		return fmt.Errorf("save track returned empty path without error")
	}

	// Ensure temporary file is cleaned up eventually
	defer func() {
		app.activeFilesMutex.Lock()
		delete(app.activeFiles, tempLocation) // Remove from active tracking
		app.activeFilesMutex.Unlock()
		errRemove := os.Remove(tempLocation)
		if errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			app.LogError(fmt.Sprintf("[%s] cleanup", track.StoreUrl()), fmt.Errorf("failed to remove temp file %s: %w", tempLocation, errRemove))
		} else {
			// fmt.Printf("Cleaned up temporary file: %s\n", tempLocation) // Reduce verbosity
		}
	}()

	// 2. Tag the temporary track file
	if err = app.tagTrack(tempLocation, track, coverPath); err != nil {
		return fmt.Errorf("tag track %s: %w", tempLocation, err)
	}

	// 3. Send the tagged track via Telegram
	// Check if bot is available before attempting to send
	if app.telegramBot == nil {
		// Log error but don't necessarily stop processing if bot isn't critical for other operations
		app.LogError(fmt.Sprintf("[%s] send telegram", track.StoreUrl()), ErrTelegramBotMissing)
		// Decide if this should be a fatal error for the track processing
		// return ErrTelegramBotMissing
	} else {
		// Send the tagged track via Telegram
		err = app.sendTrackViaTelegram(chatID, tempLocation)
		if err != nil {
			// Log the error but potentially continue if sending isn't critical
			app.LogError(fmt.Sprintf("[%s] send telegram", track.StoreUrl()), fmt.Errorf("send track via telegram %s: %w", tempLocation, err))
			// return fmt.Errorf("send track via telegram %s: %w", tempLocation, err)
		}
	}

	// 4. Temporary file is deleted by the deferred os.Remove

	return nil
}

func (app *application) cleanup(downloadsDir string) {
	if downloadsDir != app.config.DownloadsDirectory {
		os.Remove(downloadsDir)
	}
}

// func ForPaginated[T any](
// 	entityId int64,
// 	params string,
// 	fetchPage func(id int64, page int, params string) (results *beatport.Paginated[T], err error),
// 	processItem func(item T, i int) error,
// ) error {
// 	page := 1
// 	for {
// 		paginated, err := fetchPage(entityId, page, params)
// 		if err != nil {
// 			return fmt.Errorf("fetch page: %w", err)
// 		}

// 		for i, item := range paginated.Results {
// 			if err := processItem(item, i); err != nil {
// 				return fmt.Errorf("process item: %w", err)
// 			}
// 		}

// 		if paginated.Next == nil {
// 			break
// 		}
// 		page++
// 	}
// 	return nil
// }

// handleUrl parses the URL and routes it to the appropriate handler, passing the chatID.
func (app *application) handleUrl(url string, chatID int64) { // Add chatID parameter
	link, err := app.bp.ParseUrl(url)
	if err != nil {
		app.errorLogWrapper(url, "parse url", err)
		// Optionally send error back via Telegram
		if app.telegramBot != nil {
			// Need to import "github.com/go-telegram/bot" and "fmt" if not already done at top
			_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{ // Use botCtx
				ChatID: chatID,
				Text:   fmt.Sprintf("❌ Error parsing URL %s: %v", url, err),
			})
		}
		return // Keep the return statement
	}

	var inst *beatport.Beatport
	switch link.Store {
	case beatport.StoreBeatport:
		inst = app.bp
	case beatport.StoreBeatsource:
		inst = app.bs
	default:
		app.LogError("handle URL", ErrUnsupportedLinkStore)
		return
	}

	switch link.Type {
	case beatport.TrackLink:
		app.handleTrackLink(inst, link, chatID) // Pass chatID
	case beatport.ReleaseLink:
		// TODO: Implement ReleaseLink support
		_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Beatport Release links are not supported yet",
		})
	case beatport.PlaylistLink:
		// TODO: Implement PlaylistLink support
		_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Beatport Playlist links are not supported yet",
		})
	case beatport.ChartLink:
		// TODO: Implement ChartLink support
		_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Beatport Chart links are not supported yet",
		})
	case beatport.LabelLink:
		// TODO: Implement LabelLink support
		_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Beatport Label links are not supported yet",
		})
	case beatport.ArtistLink:
		// TODO: Implement ArtistLink support
		_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Beatport Artist links are not supported yet",
		})
	default:
		app.LogError("handle URL", ErrUnsupportedLinkType)
		if app.telegramBot != nil {
			_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   fmt.Sprintf("❌ Unsupported link type for URL: %s", url),
			})
		}
	}
}

// --- Update signatures for all handle*Link functions to accept chatID ---
func (app *application) handleTrackLink(inst *beatport.Beatport, link *beatport.Link, chatID int64) { // Add chatID
	track, err := inst.GetTrack(link.ID)
	if err != nil {
		app.errorLogWrapper(link.Original, "fetch track", err)
		// Optionally send error back via Telegram
		if app.telegramBot != nil {
			_, _ = app.telegramBot.SendMessage(app.botCtx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   fmt.Sprintf("❌ Error fetching track %s: %v", link.Original, err),
			})
		}
		return
	}

	release, err := inst.GetRelease(track.Release.ID)
	if err != nil {
		app.errorLogWrapper(link.Original, "fetch track release", err)
		return
	}
	track.Release = *release

	downloadsDir, err := app.setupDownloadsDirectory(app.config.DownloadsDirectory, release)
	if err != nil {
		app.errorLogWrapper(link.Original, "setup downloads directory", err)
		return
	}

	wg := sync.WaitGroup{}
	app.downloadWorker(&wg, func() {
		var cover string
		if app.requireCover(true, true) {
			cover, err = app.downloadCover(track.Release.Image, downloadsDir)
			if err != nil {
				app.errorLogWrapper(link.Original, "download track release cover", err)
			}
		}

		if err := app.handleTrack(inst, track, downloadsDir, chatID); err != nil {
			app.errorLogWrapper(link.Original, "handle track", err)
			os.Remove(cover)
			return
		}

		if err := app.handleCoverFile(cover); err != nil {
			app.errorLogWrapper(link.Original, "handle cover file", err)
			return
		}
	})
	wg.Wait()

	app.cleanup(downloadsDir)
}

// func (app *application) handleReleaseLink(inst *beatport.Beatport, link *beatport.Link) {
// 	release, err := inst.GetRelease(link.ID)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "fetch release", err)
// 		return
// 	}

// 	downloadsDir, err := app.setupDownloadsDirectory(app.config.DownloadsDirectory, release)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "setup downloads directory", err)
// 		return
// 	}

// 	var cover string
// 	if app.requireCover(true, true) {
// 		app.semAcquire(app.downloadSem)
// 		cover, err = app.downloadCover(release.Image, downloadsDir)
// 		if err != nil {
// 			app.errorLogWrapper(link.Original, "download release cover", err)
// 		}
// 		app.semRelease(app.downloadSem)
// 	}

// 	wg := sync.WaitGroup{}
// 	for _, trackUrl := range release.TrackUrls {
// 		app.downloadWorker(&wg, func() {
// 			trackLink, err := inst.ParseUrl(trackUrl)
// 			if err != nil {
// 				app.errorLogWrapper(link.Original, "parse track url", err)
// 				return
// 			}

// 			track, err := inst.GetTrack(trackLink.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackUrl, "fetch release track", err)
// 				return
// 			}
// 			trackStoreUrl := track.StoreUrl()
// 			track.Release = *release

// 			if err := app.handleTrack(inst, track, downloadsDir, cover); err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "handle track", err)
// 				return
// 			}
// 		})
// 	}
// 	wg.Wait()

// 	if err := app.handleCoverFile(cover); err != nil {
// 		app.errorLogWrapper(link.Original, "handle cover file", err)
// 		return
// 	}

// 	app.cleanup(downloadsDir)
// }

// func (app *application) handlePlaylistLink(inst *beatport.Beatport, link *beatport.Link) {
// 	playlist, err := inst.GetPlaylist(link.ID)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "fetch playlist", err)
// 		return
// 	}

// 	downloadsDir, err := app.setupDownloadsDirectory(app.config.DownloadsDirectory, playlist)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "setup downloads directory", err)
// 		return
// 	}

// 	wg := sync.WaitGroup{}
// 	err = ForPaginated[beatport.PlaylistItem](link.ID, "", inst.GetPlaylistItems, func(item beatport.PlaylistItem, i int) error {
// 		app.downloadWorker(&wg, func() {
// 			trackStoreUrl := item.Track.StoreUrl()

// 			release, err := inst.GetRelease(item.Track.Release.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "fetch track release", err)
// 				return
// 			}
// 			item.Track.Release = *release

// 			trackDownloadsDir := downloadsDir
// 			trackFull, err := inst.GetTrack(item.Track.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "fetch full track", err)
// 				return
// 			}
// 			item.Track.Number = trackFull.Number
// 			if app.config.SortByContext && app.config.ForceReleaseDirectories {
// 				trackDownloadsDir, err = app.setupDownloadsDirectory(downloadsDir, release)
// 				if err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "setup track release directory", err)
// 					return
// 				}
// 			}

// 			var cover string
// 			if app.requireCover(true, app.config.ForceReleaseDirectories) {
// 				cover, err = app.downloadCover(item.Track.Release.Image, trackDownloadsDir)
// 				if err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "download track release cover", err)
// 				} else if !app.config.ForceReleaseDirectories {
// 					defer os.Remove(cover)
// 				}
// 			}

// 			if err := app.handleTrack(inst, &item.Track, trackDownloadsDir, cover); err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "handle track", err)
// 				os.Remove(cover)
// 				app.cleanup(trackDownloadsDir)
// 				return
// 			}

// 			if app.config.ForceReleaseDirectories {
// 				if err := app.handleCoverFile(cover); err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "handle track release cover file", err)
// 					return
// 				}
// 			}

// 			app.cleanup(trackDownloadsDir)
// 		})
// 		return nil
// 	})

// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "handle playlist items", err)
// 		return
// 	}

// 	wg.Wait()
// }

// func (app *application) handleChartLink(inst *beatport.Beatport, link *beatport.Link) {
// 	chart, err := inst.GetChart(link.ID)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "fetch chart", err)
// 		return
// 	}

// 	downloadsDir, err := app.setupDownloadsDirectory(app.config.DownloadsDirectory, chart)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "setup downloads directory", err)
// 		return
// 	}
// 	wg := sync.WaitGroup{}

// 	if app.requireCover(false, true) {
// 		app.downloadWorker(&wg, func() {
// 			cover, err := app.downloadCover(chart.Image, downloadsDir)
// 			if err != nil {
// 				app.errorLogWrapper(link.Original, "download chart cover", err)
// 			}
// 			if err := app.handleCoverFile(cover); err != nil {
// 				app.errorLogWrapper(link.Original, "handle cover file", err)
// 				return
// 			}
// 		})
// 	}

// 	err = ForPaginated[beatport.Track](link.ID, "", inst.GetChartTracks, func(track beatport.Track, i int) error {
// 		app.downloadWorker(&wg, func() {
// 			trackStoreUrl := track.StoreUrl()

// 			release, err := inst.GetRelease(track.Release.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "fetch track release", err)
// 				return
// 			}
// 			track.Release = *release

// 			trackDownloadsDir := downloadsDir
// 			trackFull, err := inst.GetTrack(track.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "fetch full track", err)
// 				return
// 			}
// 			track.Number = trackFull.Number
// 			if app.config.SortByContext && app.config.ForceReleaseDirectories {
// 				trackDownloadsDir, err = app.setupDownloadsDirectory(downloadsDir, release)
// 				if err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "setup track release directory", err)
// 					return
// 				}
// 			}

// 			var cover string
// 			if app.requireCover(true, app.config.ForceReleaseDirectories) {
// 				cover, err = app.downloadCover(track.Release.Image, trackDownloadsDir)
// 				if err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "download track release cover", err)
// 				} else if !app.config.ForceReleaseDirectories {
// 					defer os.Remove(cover)
// 				}
// 			}

// 			if err := app.handleTrack(inst, &track, trackDownloadsDir, cover); err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "handle track", err)
// 				os.Remove(cover)
// 				app.cleanup(trackDownloadsDir)
// 				return
// 			}

// 			if app.config.ForceReleaseDirectories {
// 				if err := app.handleCoverFile(cover); err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "handle track release cover file", err)
// 					return
// 				}
// 			}

// 			app.cleanup(trackDownloadsDir)
// 		})
// 		return nil
// 	})

// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "handle playlist items", err)
// 		return
// 	}

// 	wg.Wait()
// }

// func (app *application) handleLabelLink(inst *beatport.Beatport, link *beatport.Link) {
// 	label, err := inst.GetLabel(link.ID)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "fetch label", err)
// 		return
// 	}

// 	downloadsDir, err := app.setupDownloadsDirectory(app.config.DownloadsDirectory, label)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "setup downloads directory", err)
// 		return
// 	}

// 	err = ForPaginated[beatport.Release](link.ID, link.Params, inst.GetLabelReleases, func(release beatport.Release, i int) error {
// 		app.background(func() {
// 			releaseStoreUrl := release.StoreUrl()
// 			releaseDir, err := app.setupDownloadsDirectory(downloadsDir, &release)
// 			if err != nil {
// 				app.errorLogWrapper(releaseStoreUrl, "setup release downloads directory", err)
// 				return
// 			}

// 			var cover string
// 			if app.requireCover(true, true) {
// 				app.semAcquire(app.downloadSem)
// 				cover, err = app.downloadCover(release.Image, releaseDir)
// 				if err != nil {
// 					app.errorLogWrapper(releaseStoreUrl, "download release cover", err)
// 				}
// 				app.semRelease(app.downloadSem)
// 			}

// 			wg := sync.WaitGroup{}
// 			err = ForPaginated[beatport.Track](release.ID, "", inst.GetReleaseTracks, func(track beatport.Track, i int) error {
// 				app.downloadWorker(&wg, func() {
// 					trackStoreUrl := track.StoreUrl()
// 					t, err := inst.GetTrack(track.ID)
// 					if err != nil {
// 						app.errorLogWrapper(trackStoreUrl, "fetch full track", err)
// 						return
// 					}
// 					t.Release = release

// 					if err := app.handleTrack(inst, t, releaseDir, cover); err != nil {
// 						app.errorLogWrapper(trackStoreUrl, "handle track", err)
// 						return
// 					}
// 				})
// 				return nil
// 			})
// 			if err != nil {
// 				app.errorLogWrapper(releaseStoreUrl, "handle release tracks", err)
// 				os.Remove(cover)
// 				app.cleanup(releaseDir)
// 				return
// 			}
// 			wg.Wait()

// 			app.cleanup(releaseDir)

// 			if err := app.handleCoverFile(cover); err != nil {
// 				app.errorLogWrapper(releaseStoreUrl, "handle cover file", err)
// 				return
// 			}
// 		})
// 		return nil
// 	})

// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "handle label releases", err)
// 		return
// 	}
// }

// func (app *application) handleArtistLink(inst *beatport.Beatport, link *beatport.Link) {
// 	artist, err := inst.GetArtist(link.ID)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "fetch artist", err)
// 		return
// 	}

// 	downloadsDir, err := app.setupDownloadsDirectory(app.config.DownloadsDirectory, artist)
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "setup downloads directory", err)
// 		return
// 	}

// 	wg := sync.WaitGroup{}
// 	err = ForPaginated[beatport.Track](link.ID, link.Params, inst.GetArtistTracks, func(track beatport.Track, i int) error {
// 		app.downloadWorker(&wg, func() {
// 			trackStoreUrl := track.StoreUrl()
// 			t, err := inst.GetTrack(track.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "fetch full track", err)
// 				return
// 			}

// 			release, err := inst.GetRelease(track.Release.ID)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "fetch track release", err)
// 				return
// 			}
// 			t.Release = *release

// 			releaseDir, err := app.setupDownloadsDirectory(downloadsDir, release)
// 			if err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "setup track release downloads directory", err)
// 				return
// 			}

// 			var cover string
// 			if app.requireCover(true, true) {
// 				cover, err = app.downloadCover(release.Image, releaseDir)
// 				if err != nil {
// 					app.errorLogWrapper(trackStoreUrl, "download track release cover", err)
// 				}
// 			}

// 			if err := app.handleTrack(inst, t, releaseDir, cover); err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "handle track", err)
// 				os.Remove(cover)
// 				app.cleanup(releaseDir)
// 				return
// 			}

// 			if err := app.handleCoverFile(cover); err != nil {
// 				app.errorLogWrapper(trackStoreUrl, "handle cover file", err)
// 				return
// 			}

// 			app.cleanup(releaseDir)
// 		})
// 		return nil
// 	})
// 	if err != nil {
// 		app.errorLogWrapper(link.Original, "handle artist tracks", err)
// 		return
// 	}

// 	wg.Wait()
// }
