package main

import (
	"fmt"
	"go.senan.xyz/taglib"
	// "lucasduarte0/beatportdl-telegram/config" // Removed unused import
	"lucasduarte0/beatportdl-telegram/internal/beatport"
	// "os" // Removed unused import
	"path/filepath"
	"strconv"
	// "strings" // Removed unused import
)

func (app *application) tagTrack(location string, track *beatport.Track, coverPath string) error { // Keep coverPath param for now, even if unused, to maintain signature
	fileExt := filepath.Ext(location)
	if !app.config.FixTags {
		return nil
	}

	// 1. Prepare mapping values (existing logic)
	subgenre := ""
	if track.Subgenre != nil {
		subgenre = track.Subgenre.Name
	}
	mappingValues := map[string]string{
		"track_id":       strconv.Itoa(int(track.ID)),
		"track_url":      track.StoreUrl(),
		"track_name":     fmt.Sprintf("%s (%s)", track.Name.String(), track.MixName.String()),
		"track_artists":  track.Artists.Display(0, ""),
		"track_remixers": track.Remixers.Display(0, ""),
		"track_artists_limited": track.Artists.Display(
			app.config.ArtistsLimit,
			app.config.ArtistsShortForm,
		),
		"track_remixers_limited": track.Remixers.Display(
			app.config.ArtistsLimit,
			app.config.ArtistsShortForm,
		),
		"track_number":              strconv.Itoa(track.Number),
		"track_number_with_padding": beatport.NumberWithPadding(track.Number, track.Release.TrackCount, app.config.TrackNumberPadding),
		"track_number_with_total":   fmt.Sprintf("%d/%d", track.Number, track.Release.TrackCount),
		"track_genre":               track.Genre.Name,
		"track_subgenre":            subgenre,
		"track_genre_with_subgenre": track.GenreWithSubgenre("|"),
		"track_subgenre_or_genre":   track.SubgenreOrGenre(),
		"track_key":                 track.Key.Display(app.config.KeySystem),
		"track_bpm":                 strconv.Itoa(track.BPM),
		"track_isrc":                track.ISRC,

		"release_id":   strconv.Itoa(int(track.Release.ID)),
		"release_url":  track.Release.StoreUrl(),
		"release_name": track.Release.Name.String(),
		"release_artists": track.Release.Artists.Display(
			0,
			"",
		),
		"release_remixers": track.Release.Remixers.Display(
			0,
			"",
		),
		"release_artists_limited": track.Release.Artists.Display(
			app.config.ArtistsLimit,
			app.config.ArtistsShortForm,
		),
		"release_remixers_limited": track.Release.Remixers.Display(
			app.config.ArtistsLimit,
			app.config.ArtistsShortForm,
		),
		"release_date":        track.Release.Date,
		"release_year":        track.Release.Year(),
		"release_track_count": strconv.Itoa(track.Release.TrackCount),
		"release_track_count_with_padding": beatport.NumberWithPadding(
			track.Release.TrackCount, track.Release.TrackCount, app.config.TrackNumberPadding,
		),
		"release_catalog_number": track.Release.CatalogNumber.String(),
		"release_upc":            track.Release.UPC,
		"release_label":          track.Release.Label.Name,
		"release_label_url":      track.Release.Label.StoreUrl(),
	}

	// 2. Create the map for WriteTags
	tagsToWrite := make(map[string][]string)
	var currentMappings map[string]string

	// Determine which mapping to use based on file extension
	if fileExt == ".flac" {
		currentMappings = app.config.TagMappings["flac"]
	} else if fileExt == ".m4a" {
		currentMappings = app.config.TagMappings["m4a"]
	} else {
		// Fallback or default mapping if needed, e.g., for MP3
		if defaultMapping, ok := app.config.TagMappings["default"]; ok {
			currentMappings = defaultMapping
		} else {
			// If no default, maybe skip tagging or return an error
			return fmt.Errorf("no tag mapping found for file extension: %s", fileExt)
		}
	}

	// Populate tagsToWrite using the selected mapping
	for field, property := range currentMappings {
		value := mappingValues[field]
		if value != "" {
			// The new API takes []string, and handles standard/non-standard keys.
			tagsToWrite[property] = []string{value}
		}
	}

	// 3. Write tags using the new API
	// Using 0 for options (no Clear, no DiffBeforeWrite) as per the simplest doc example.
	// Add options like taglib.Clear if needed based on app config.
	var writeOptions taglib.WriteOption = 0 // Correct type for options
	// Example: if app.config.ClearExistingTags { writeOptions |= taglib.Clear }

	err := taglib.WriteTags(location, tagsToWrite, writeOptions)
	if err != nil {
		return fmt.Errorf("failed to write tags: %w", err)
	}

	// 4. Picture handling is removed as the documented API (WriteTags) doesn't cover it,
	// and the old method (file.SetPicture/Save) is incompatible with WriteTags.

	return nil
}
