//go:build darwin && cgo

package main

/*
// A bare executable that owns its Local Network responsibility needs its own
// usage description. Embed before signing; the app bundle's plist is not ours.
__attribute__((section("__TEXT,__info_plist"), used))
static const char nocx_helper_info[] =
 "<?xml version=\"1.0\" encoding=\"UTF-8\"?>"
 "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">"
 "<plist version=\"1.0\"><dict>"
 "<key>CFBundleIdentifier</key><string>dev.nocx.helper</string>"
 "<key>CFBundleName</key><string>nocx-helper</string>"
 "<key>NSLocalNetworkUsageDescription</key>"
 "<string>nocx-helper connects your terminal sessions to servers on your local network.</string>"
 "</dict></plist>";
*/
import "C"
