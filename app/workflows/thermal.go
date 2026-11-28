// Thermal mode: dji-thermal-convert turns DJI R-JPEGs into °C TIFFs before ODM.
// Its settings are stored as thermal-* ODM flags, so restarts keep them.

package workflows

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const thermalFlagPrefix = "--thermal-"

var thermalSettings = []string{"ambient", "reflection", "humidity", "distance", "emissivity"}

// Global seam leveling rewrites pixel values, which corrupts temperatures.
const thermalSeamLevelingFlag = "--texturing-skip-global-seam-leveling"

// SplitThermalFlags turns --thermal-<name>=<value> into converter args "--<name> <value>".
func SplitThermalFlags(flags []string) (odmFlags, converterArgs []string) {
	for _, flag := range flags {
		setting, ok := strings.CutPrefix(flag, thermalFlagPrefix)
		if !ok {
			odmFlags = append(odmFlags, flag)
			continue
		}
		name, value, _ := strings.Cut(setting, "=")
		converterArgs = append(converterArgs, "--"+name, value)
	}
	return odmFlags, converterArgs
}

// ValidateThermalFlags also rejects radiometric calibration, as images are already °C.
func ValidateThermalFlags(mode string, flags []string) error {
	for _, flag := range flags {
		calibrates := strings.HasPrefix(flag, "--radiometric-calibration") && flag != "--radiometric-calibration=none"
		if mode == ProcessingModeThermal && calibrates {
			return fmt.Errorf("radiometric-calibration cannot be used with processingMode %q: images are already converted to °C", ProcessingModeThermal)
		}
		setting, ok := strings.CutPrefix(flag, thermalFlagPrefix)
		if !ok {
			continue
		}
		if mode != ProcessingModeThermal {
			return fmt.Errorf("option %q requires processingMode %q", strings.TrimPrefix(flag, "--"), ProcessingModeThermal)
		}
		name, value, _ := strings.Cut(setting, "=")
		if !slices.Contains(thermalSettings, name) {
			return fmt.Errorf("unknown thermal option %q (supported: thermal-%s)", "thermal-"+name, strings.Join(thermalSettings, ", thermal-"))
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("option thermal-%s must be a number, got %q", name, value)
		}
	}
	return nil
}
