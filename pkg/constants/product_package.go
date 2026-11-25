package constants

const (
	ProductPackageDirectoryHeader         = "X-A2MaTeX-Package-Directory"
	ProductPackageDirectoryHeaderMaxBytes = 4 * 1024
	ProductPackageFilenameMaxBytes        = 255
	ProductPackageMaxBytes                = 4 * 1024 * 1024
	ProductPackageExpandedMaxBytes        = 2 * 1024 * 1024
	ProductPackageGCMTagBytes             = 16
	ProductPackageNonceBytes              = 12
	ProductPackageEncryptionAlgorithm     = "AES-256-GCM"
	ProductPackageEntryCount              = 3
	ProductPackageReadChunkBytes          = 32 * 1024
	ProductPackageZIPEncryptionFlags      = 1<<0 | 1<<6 | 1<<13
	ProductPackageManifestEntry           = "manifest.json"
	ProductPackageWorkflowEntry           = "workflow.enc"
	ProductPackageSchemaEntry             = "input-schema.json"
)
