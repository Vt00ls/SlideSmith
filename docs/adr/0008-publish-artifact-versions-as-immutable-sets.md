# Publish artifact versions as immutable sets

An Artifact Version is one immutable Task-owned publication containing a manifest of all related Artifacts, rather than a revision number attached independently to each file. Individual Artifacts belong to exactly one published version, manual edits create a new version with parent lineage instead of overwriting files, and Share Links target the Artifact Version even when a recipient downloads one member. Ownership is resolved through Personal Workspace to Task to Artifact Version, avoiding duplicated User ownership fields on every stored object.
