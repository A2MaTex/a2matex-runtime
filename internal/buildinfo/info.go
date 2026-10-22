package buildinfo

const Name = "a2matex-runtime"

var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
}

func Current() Info {
	return Info{
		Name:      Name,
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
	}
}
