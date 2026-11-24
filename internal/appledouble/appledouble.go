// Package appledouble knows the "._" files in which macOS keeps a file's
// extended attributes on filesystems without them, such as FAT and exFAT.
// macOS writes and removes such a file together with the file it belongs to.
package appledouble

// Prefix starts the name of every AppleDouble file.
const Prefix = "._"

// Pattern is the exclude pattern that matches AppleDouble files.
const Pattern = Prefix + "*"
