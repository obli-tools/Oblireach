package engine

// SHA-256 of every third-party DLL the engine may load from the folder of
// the executable (the codec DLLs shipped by the agent MSI and their MinGW
// runtime dependencies). A file whose name is not listed here, or whose
// content does not match one of its hashes, is never loaded
// (codecdll_windows.go): the encoder then falls back (Media Foundation,
// JPEG).
//
// UPDATE THESE PINS WHENEVER A SHIPPED DLL CHANGES (agent/dist, the agent
// folder, libvpx-prebuilt, svtav1-prebuilt). TestCodecDLLPinsMatchRepo
// recomputes the hashes of the DLL files in the repository and fails on any
// DLL without a matching pin. To pin a new build:
//
//	sha256sum agent/dist/*.dll
//
// A name may carry several hashes (another architecture, a transition
// between two builds). libx265 (GPL, never shipped) has no pin: an H.265
// build must add the hash of the DLL it ships.
var codecDLLPins = map[string][]string{
	// Cisco OpenH264 2.4.1 win64 (Authenticode: Cisco WebEx LLC)
	"openh264-2.4.1-win64.dll": {"081b0c081480d177cbfddfbc90b1613640e702f875897b30d8de195cde73dd34"},
	// libvpx (MSYS2 mingw-w64-x86_64-libvpx, libvpx-prebuilt)
	"libvpx-1.dll": {"33a1b39ccef477d6afe64eafd5743ff35d58342ed0fb6b911dc7be64e006b6fd"},
	// SVT-AV1 (MSYS2 mingw-w64-x86_64-svt-av1, svtav1-prebuilt)
	"libsvtav1enc-2.dll": {"1b75c6505dbfca91d83e7002be3b90ab123524c7e813df813ccc6885f8bef28d"},
	// MinGW-w64 winpthreads, x86-64 (the only runtime DLL the codec DLLs
	// import: libvpx). TDM-GCC 10.3 ships it as bin\libwinpthread_64-1.dll
	// (its bin\libwinpthread-1.dll is the i386 build): it is shipped under
	// the name libvpx imports.
	"libwinpthread-1.dll": {"fecd1b1e3eeddd50632eb6e3b1f477b13891b8f9531cc6557dccc9bbc968d04c"},
}
