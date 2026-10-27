package s3

import (
	"strings"
)

// alwaysExcludePatterns are unconditionally prepended to both download and
// image-count filters, regardless of user settings or useDefaultExcludes.
// They protect ScaleODM's own output directories from being re-ingested on a
// rerun, and match the paths the upload script writes into the write S3 path.
var alwaysExcludePatterns = []string{
	"output/**", "**/output/**",
	"odm/**", "**/odm/**",
}

// imageIncludePatterns is the canonical rclone filter list for input imagery
// and supported archive types. Mirrored as `+ <pattern>` lines in the
// generated --filter-from file.
var imageIncludePatterns = []string{
	"*.jpg",
	"*.jpeg",
	"*.JPG",
	"*.JPEG",
	"*.tiff",
	"*.tif",
	"*.TIFF",
	"*.TIF",
	"*.zip",
	"*.ZIP",
	"*.tar.gz",
	"*.tar",
	"*.TAR.GZ",
	"*.TAR",
}

// renderRcloneFilterFile builds the contents of a rclone --filter-from file.
// Order matters: excludes go first (so they win over the catch-all includes
// below), then includes for image/archive extensions, then a final catch-all
// that drops everything else.
func renderRcloneFilterFile(excludePatterns []string) string {
	var b strings.Builder
	for _, p := range excludePatterns {
		b.WriteString("- ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	for _, p := range imageIncludePatterns {
		b.WriteString("+ ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	b.WriteString("- *\n")
	return b.String()
}

// DownloadFilterRules returns the rclone filter file for the download script:
// alwaysExcludePatterns, then excludePatterns (validated by the API layer).
func DownloadFilterRules(excludePatterns []string) string {
	patterns := make([]string, 0, len(alwaysExcludePatterns)+len(excludePatterns))
	patterns = append(patterns, alwaysExcludePatterns...)
	patterns = append(patterns, excludePatterns...)
	return renderRcloneFilterFile(patterns)
}
