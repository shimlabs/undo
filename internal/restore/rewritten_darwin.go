package restore

// clonesKeepExtendedAttributes says whether the store's copies keep a file's
// extended attributes, which clonefile copies on macOS. The same file with a
// new change time may then have new ones, which its content does not show.
const clonesKeepExtendedAttributes = true
