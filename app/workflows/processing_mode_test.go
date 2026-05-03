package workflows

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsImplementedProcessingMode(t *testing.T) {
	assert.True(t, IsImplementedProcessingMode(ProcessingModeStandard))
	assert.False(t, IsImplementedProcessingMode(ProcessingModeMergeExisting))
	assert.False(t, IsImplementedProcessingMode("single-task"))
	assert.False(t, IsImplementedProcessingMode("multi-task"))
	assert.False(t, IsImplementedProcessingMode("nonsense"))
}

func TestIsReservedProcessingMode(t *testing.T) {
	assert.True(t, IsReservedProcessingMode(ProcessingModeMergeExisting))
	assert.True(t, IsReservedProcessingMode(ProcessingModeThermal))
	assert.True(t, IsReservedProcessingMode(ProcessingModeCityScale))
	assert.False(t, IsReservedProcessingMode(ProcessingModeStandard))
	assert.False(t, IsReservedProcessingMode("nonsense"))
}

func TestProcessingModeStandardValue(t *testing.T) {
	assert.Equal(t, "standard", ProcessingModeStandard)
}

func TestValidateS3ScanDepth(t *testing.T) {
	cases := []struct {
		name    string
		input   int
		want    int
		wantErr bool
	}{
		{name: "zero resolves to default", input: 0, want: DefaultS3ScanDepth},
		{name: "one passes through", input: 1, want: 1},
		{name: "max boundary ok", input: MaxS3ScanDepth, want: MaxS3ScanDepth},
		{name: "negative rejected", input: -1, wantErr: true},
		{name: "above max rejected", input: MaxS3ScanDepth + 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateS3ScanDepth(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestComposeExcludePatterns_DefaultsOnPrependsCanonicalSet(t *testing.T) {
	user := []string{"scratch/**", "*.bak"}
	got := ComposeExcludePatterns(true, user)

	require.Greater(t, len(got), len(user))
	// Canonical set must come first so user patterns layer on top.
	assert.Equal(t, DefaultProjectExcludes[0], got[0])
	assert.Contains(t, got, "odm_orthophoto/**")
	assert.Contains(t, got, "submodels/**")
	assert.Contains(t, got, "thumbs/**")
	assert.Contains(t, got, "**/thumbs/**")
	assert.Contains(t, got, "all.zip")
	assert.Contains(t, got, "scratch/**")
	assert.Contains(t, got, "*.bak")
}

func TestComposeExcludePatterns_DefaultsOffOmitsCanonicalSet(t *testing.T) {
	user := []string{"only/this/**"}
	got := ComposeExcludePatterns(false, user)

	assert.Equal(t, []string{"only/this/**"}, got)
	assert.NotContains(t, got, "odm_orthophoto/**")
}

func TestValidateExcludePattern(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "simple dir glob", input: "odm_orthophoto/**"},
		{name: "double-star prefix", input: "**/odm_dem/**"},
		{name: "extension glob", input: "*.bak"},
		{name: "exact basename", input: "all.zip"},
		{name: "char class", input: "[abc].jpg"},
		{name: "question mark", input: "?.jpg"},

		{name: "empty", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "newline", input: "foo\nbar", wantErr: true},
		{name: "carriage return", input: "foo\rbar", wantErr: true},
		{name: "path traversal", input: "../etc/**", wantErr: true},
		{name: "absolute path", input: "/etc/passwd", wantErr: true},
		{name: "command substitution", input: "$(rm -rf /)", wantErr: true},
		{name: "backtick", input: "`whoami`", wantErr: true},
		{name: "semicolon", input: "foo;rm -rf /", wantErr: true},
		{name: "pipe", input: "foo|bar", wantErr: true},
		{name: "double quote", input: `foo"bar`, wantErr: true},
		{name: "single quote", input: "foo'bar", wantErr: true},
		{name: "backslash", input: `foo\bar`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExcludePattern(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
