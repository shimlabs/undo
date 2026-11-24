package restore

// hasExtendedAttributesInClones says whether the store's clones keep a file's
// extended attributes. FICLONE keeps only content on Linux; copy mode keeps
// extended attributes too, which rewrittenPaths checks.
const hasExtendedAttributesInClones = false
