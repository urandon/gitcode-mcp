//go:build darwin

package localauthority_test

func runNativeNetworkProbe() int      { return 78 } // Linux observer cohort only
func runNativeEscapeProbe(string) int { return 78 }
