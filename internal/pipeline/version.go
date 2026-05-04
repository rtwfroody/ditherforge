package pipeline

// VersionSemver is the bare semver portion of the application version.
// Keep this in sync with Version below; the release workflow greps Version.
const VersionSemver = "0.8.2"

// Version is the application version string shown in UIs and CLI --version.
const Version = "ditherforge " + VersionSemver
