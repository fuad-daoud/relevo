package release

// DownloadBase is where relevo's published release assets live: a constant,
// so no environment variable can redirect a binary download.
const DownloadBase = "https://github.com/fuad-daoud/relevo/releases/download"

// AssetURLs returns the release archive and checksums URL for tag on
// goos/goarch. Pure; callers pass only a tag IsReleaseTag accepts.
func AssetURLs(tag, goos, goarch string) (archive, checksums string) {
	return assetURLsFrom(DownloadBase, tag, goos, goarch)
}

// assetURLsFrom is AssetURLs against an arbitrary base, so tests can pass an httptest URL.
func assetURLsFrom(base, tag, goos, goarch string) (archive, checksums string) {
	root := base + "/" + tag
	return root + "/relevo_" + tag + "_" + goos + "_" + goarch + ".tar.gz",
		root + "/checksums.txt"
}
