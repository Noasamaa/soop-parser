package model

// Quality is one browser-playable stream variant.
type Quality struct {
	Label     string `json:"label"`
	Name      string `json:"name"`
	DirectURL string `json:"direct_url,omitempty"`
	PlayURL   string `json:"play_url,omitempty"`
	Protocol  string `json:"protocol"` // hls | progressive
}

// Result is a resolved live/VOD stream from any platform.
type Result struct {
	Channel           string    `json:"channel"`
	BNO               string    `json:"bno"`
	Title             string    `json:"title,omitempty"`
	Author            string    `json:"author,omitempty"`
	PasswordProtected bool      `json:"password_protected"`
	Platform          string    `json:"platform"`
	IsLive            bool      `json:"is_live"`
	Qualities         []Quality `json:"qualities"`
}
