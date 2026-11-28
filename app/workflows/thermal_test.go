package workflows

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitThermalFlags(t *testing.T) {
	odm, converter := SplitThermalFlags([]string{"--dsm", "--thermal-ambient=27.5", "--orthophoto-resolution=10", "--thermal-distance=100"})
	assert.Equal(t, []string{"--dsm", "--orthophoto-resolution=10"}, odm)
	assert.Equal(t, []string{"--ambient", "27.5", "--distance", "100"}, converter)
}

func TestValidateThermalFlags(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		flags   []string
		wantErr string
	}{
		{"all settings", ProcessingModeThermal, []string{"--thermal-ambient=27", "--thermal-reflection=-5.5", "--thermal-humidity=80", "--thermal-distance=100", "--thermal-emissivity=0.95"}, ""},
		{"thermal mode without settings", ProcessingModeThermal, []string{"--dsm"}, ""},
		{"standard mode without settings", ProcessingModeStandard, []string{"--radiometric-calibration=camera"}, ""},
		{"setting outside thermal mode", ProcessingModeStandard, []string{"--thermal-ambient=27"}, "requires processingMode"},
		{"unknown setting", ProcessingModeThermal, []string{"--thermal-wind=3"}, "unknown thermal option"},
		{"non-numeric setting", ProcessingModeThermal, []string{"--thermal-ambient=warm"}, "must be a number"},
		{"radiometric calibration", ProcessingModeThermal, []string{"--radiometric-calibration=camera"}, "radiometric-calibration"},
		{"radiometric calibration none", ProcessingModeThermal, []string{"--radiometric-calibration=none"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateThermalFlags(tt.mode, tt.flags)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
