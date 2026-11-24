package restore

// hasExtendedAttributesInClones says whether the store's copies keep a file's
// extended attributes. FICLONE and copies keep only content on Linux.
const hasExtendedAttributesInClones = false
