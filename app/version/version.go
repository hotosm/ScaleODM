// Package version exposes the ScaleODM build version.
//
// Values are injected at link time by GoReleaser via -ldflags
// (`-X github.com/hotosm/scaleodm/app/version.Version=...`). The git tag of
// the published GitHub release is the canonical source - see
// .goreleaser.yaml. The "dev" fallback applies to local `go build` runs
// where no ldflag is supplied.
package version

var (
	Version   = "dev"
	BuildTime = "unknown"
)
