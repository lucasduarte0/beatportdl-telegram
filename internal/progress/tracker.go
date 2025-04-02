package progress

// TrackProgress tracks the status of an individual download
type TrackProgress struct {
	ID       string
	URL      string
	Quality  string
	Format   string
	SavePath string
	Progress float64 // 0-100
	Error    error
}