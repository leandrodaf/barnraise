package domain

// Tool recipes (ADR-0046): the optional tools Barnraise installs without
// root, each at a pinned version, from its project's official release, with
// the sha256 of every asset written here. The installer refuses an archive
// whose digest differs. The digests were computed from the downloaded
// assets and compared with the upstream checksum file where the release
// publishes one (rtk, osv-scanner, smolvm, gitleaks) or with the digest GitHub shows
// for the asset (ast-grep); mergiraf's release (Codeberg) publishes none.
// Raising a version means downloading the new assets and pinning their
// digests again. ast-grep on Linux is the glibc build: its musl build
// needs the musl loader, which glibc systems do not have.

// EnvToolsDir names the folder of the installed tools (<data dir>/tools)
// in the daemon's environment, for the layers that hide the data folder
// and must give that one back read-only.
const EnvToolsDir = "BARNRAISE_TOOLS_DIR"

// Archive formats of a release asset.
const (
	FormatTarGz = "tar.gz"
	FormatZip   = "zip"
	FormatRaw   = "raw" // the asset is the program itself
)

// ToolAsset is one release asset of a recipe.
type ToolAsset struct {
	OS     string
	Arch   string
	URL    string
	SHA256 string
	Format string
	// Strip is a leading folder of the archive's entries that is removed.
	Strip string
}

// ToolRecipe is one installable tool.
type ToolRecipe struct {
	ID      string
	Title   string
	Version string
	// Bin is the program put on the PATH; Path is where it is inside the
	// archive after Strip (default: Bin).
	Bin  string
	Path string
	// GuestTree is a folder of the archive (after Strip) that is a guest
	// root file system: absolute symbolic links there point inside the
	// guest, never at this machine, and are kept.
	GuestTree string
	License   string
	Home      string
	Assets    []ToolAsset
}

// Asset returns the asset for a system, if upstream ships one.
func (r ToolRecipe) Asset(os, arch string) (ToolAsset, bool) {
	for _, a := range r.Assets {
		if a.OS == os && a.Arch == arch {
			return a, true
		}
	}
	return ToolAsset{}, false
}

// BinPath is the program's path inside the installed folder.
func (r ToolRecipe) BinPath() string { return def0(r.Path, r.Bin) }

// ToolRecipes are the tools `barnraise tools install` knows.
func ToolRecipes() []ToolRecipe {
	return []ToolRecipe{
		{
			ID: "mergiraf", Title: "Structural merge driver", Version: "0.20.0", Bin: "mergiraf",
			License: "GPL-3.0", Home: "https://codeberg.org/mergiraf/mergiraf",
			Assets: []ToolAsset{
				{OS: "linux", Arch: "amd64", Format: FormatTarGz, SHA256: "fc77469be936b02ab9021b984a3aebc9250a21ae3d264f2ef2198eb8b31b2a7b",
					URL: "https://codeberg.org/mergiraf/mergiraf/releases/download/v0.20.0/mergiraf_x86_64-unknown-linux-musl.tar.gz"},
				{OS: "linux", Arch: "arm64", Format: FormatTarGz, SHA256: "0f153dfa85a799c0d05ee7a21834d049b3595e367ed411a56f007bdb5de8c19d",
					URL: "https://codeberg.org/mergiraf/mergiraf/releases/download/v0.20.0/mergiraf_aarch64-unknown-linux-musl.tar.gz"},
				{OS: "darwin", Arch: "arm64", Format: FormatTarGz, SHA256: "641b8a0c379d56b8b4b61070e60c3dac6e1a2fba5b59edd374a9e795b992ee5b",
					URL: "https://codeberg.org/mergiraf/mergiraf/releases/download/v0.20.0/mergiraf_aarch64-apple-darwin.tar.gz"},
				{OS: "darwin", Arch: "amd64", Format: FormatTarGz, SHA256: "86e5ef67c0212433238ad24dba86bee9495b6ed4c0f9695e2533c4f56017c7e4",
					URL: "https://codeberg.org/mergiraf/mergiraf/releases/download/v0.20.0/mergiraf_x86_64-apple-darwin.tar.gz"},
			},
		},
		{
			ID: "rtk", Title: "Command output compressor", Version: "1.2.1", Bin: "rtk",
			License: "Apache-2.0, its engine BSL-1.1", Home: "https://github.com/rtk-ai/rtk",
			Assets: []ToolAsset{
				{OS: "linux", Arch: "amd64", Format: FormatTarGz, SHA256: "75f64525711be935657a6b228d929c24bd9d1ab7d640ea374a8b913e5b5bac05",
					URL: "https://github.com/rtk-ai/rtk/releases/download/v1.2.1/rtk-x86_64-unknown-linux-musl.tar.gz"},
				{OS: "linux", Arch: "arm64", Format: FormatTarGz, SHA256: "cae1a911eda9e898dbb90039436e9d53922b20afb1e3e4ae341f749eb62b7781",
					URL: "https://github.com/rtk-ai/rtk/releases/download/v1.2.1/rtk-aarch64-unknown-linux-musl.tar.gz"},
				{OS: "darwin", Arch: "arm64", Format: FormatTarGz, SHA256: "3bc2a92f1d3e404ef99c200cddcc0f4917d6c4ed5b127a751938a96f57d4ed9a",
					URL: "https://github.com/rtk-ai/rtk/releases/download/v1.2.1/rtk-aarch64-apple-darwin.tar.gz"},
				{OS: "darwin", Arch: "amd64", Format: FormatTarGz, SHA256: "107e272253c0aa36c9c85fbf9dd188bdb58d86c6ba9a85b49122420baf6e4c5c",
					URL: "https://github.com/rtk-ai/rtk/releases/download/v1.2.1/rtk-x86_64-apple-darwin.tar.gz"},
			},
		},
		{
			ID: "osv-scanner", Title: "Dependency vulnerability scanner", Version: "2.7.1", Bin: "osv-scanner",
			License: "Apache-2.0", Home: "https://github.com/google/osv-scanner",
			Assets: []ToolAsset{
				{OS: "linux", Arch: "amd64", Format: FormatRaw, SHA256: "fb7dab19a8bd9bbe5c3189f09d4b7ee1820c9553a23bac202648a56efd2f6ee8",
					URL: "https://github.com/google/osv-scanner/releases/download/v2.7.1/osv-scanner_linux_amd64"},
				{OS: "linux", Arch: "arm64", Format: FormatRaw, SHA256: "983d22fbad9724514e3fcc6acc83b5fa64c5ffa6b0d9f510028f22c1b223ff81",
					URL: "https://github.com/google/osv-scanner/releases/download/v2.7.1/osv-scanner_linux_arm64"},
				{OS: "darwin", Arch: "arm64", Format: FormatRaw, SHA256: "b22c02877a811c8f78956e8643efa554932b7378f590d13f8e97d4e9448a29f3",
					URL: "https://github.com/google/osv-scanner/releases/download/v2.7.1/osv-scanner_darwin_arm64"},
				{OS: "darwin", Arch: "amd64", Format: FormatRaw, SHA256: "b8420d5485ba792626315f1bf9a5a2046e971d38eea75bf9090c38852dbc3b1b",
					URL: "https://github.com/google/osv-scanner/releases/download/v2.7.1/osv-scanner_darwin_amd64"},
			},
		},
		{
			ID: "ast-grep", Title: "Syntax-tree search and lint", Version: "0.51.3", Bin: "ast-grep",
			License: "MIT", Home: "https://github.com/ast-grep/ast-grep",
			Assets: []ToolAsset{
				{OS: "linux", Arch: "amd64", Format: FormatZip, SHA256: "22803eb0c5bf83164fb7fd76d4721d355ecf0c8466ef47342c1ec7d80897ca47",
					URL: "https://github.com/ast-grep/ast-grep/releases/download/0.51.3/app-x86_64-unknown-linux-gnu.zip"},
				{OS: "linux", Arch: "arm64", Format: FormatZip, SHA256: "5b44fd68e78744934b359b07badce347d4197ed50b341d50f1f99d056ecedb08",
					URL: "https://github.com/ast-grep/ast-grep/releases/download/0.51.3/app-aarch64-unknown-linux-gnu.zip"},
				{OS: "darwin", Arch: "arm64", Format: FormatZip, SHA256: "7954eab1e2803d1485f263377be7f359db6d3915312b5d773cd575eb009330b1",
					URL: "https://github.com/ast-grep/ast-grep/releases/download/0.51.3/app-aarch64-apple-darwin.zip"},
				{OS: "darwin", Arch: "amd64", Format: FormatZip, SHA256: "af04ed50179e51316a0139b8bb39502a3841d6c8e9fd9a4bac40987a8c7c3acd",
					URL: "https://github.com/ast-grep/ast-grep/releases/download/0.51.3/app-x86_64-apple-darwin.zip"},
			},
		},
		{
			ID: "gitleaks", Title: "Secret scanner", Version: "8.30.1", Bin: "gitleaks",
			License: "MIT", Home: "https://github.com/gitleaks/gitleaks",
			Assets: []ToolAsset{
				{OS: "linux", Arch: "amd64", Format: FormatTarGz, SHA256: "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb",
					URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_x64.tar.gz"},
				{OS: "linux", Arch: "arm64", Format: FormatTarGz, SHA256: "e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080",
					URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_arm64.tar.gz"},
				{OS: "darwin", Arch: "arm64", Format: FormatTarGz, SHA256: "b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5",
					URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_darwin_arm64.tar.gz"},
				{OS: "darwin", Arch: "amd64", Format: FormatTarGz, SHA256: "dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709",
					URL: "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_darwin_x64.tar.gz"},
			},
		},
		{
			ID: "smolvm", Title: "MicroVM runner", Version: "1.37.0", Bin: "smolvm", GuestTree: "agent-rootfs",
			License: "Apache-2.0", Home: "https://github.com/smol-machines/smolvm",
			Assets: []ToolAsset{
				{OS: "linux", Arch: "amd64", Format: FormatTarGz, Strip: "smolvm-1.37.0-linux-x86_64", SHA256: "899dce2a1145f0a0519a41e7a13a7c5cad49dc530d29a4f4aac19d57cd567775",
					URL: "https://github.com/smol-machines/smolvm/releases/download/v1.37.0/smolvm-1.37.0-linux-x86_64.tar.gz"},
				{OS: "linux", Arch: "arm64", Format: FormatTarGz, Strip: "smolvm-1.37.0-linux-arm64", SHA256: "8c5658f493c52c892251af43fff687d536e2dea7d1862aaffc9f513a91a825b1",
					URL: "https://github.com/smol-machines/smolvm/releases/download/v1.37.0/smolvm-1.37.0-linux-arm64.tar.gz"},
				{OS: "darwin", Arch: "arm64", Format: FormatTarGz, Strip: "smolvm-1.37.0-darwin-arm64", SHA256: "a36d9e28c9bf091e6325bf5a915b342f25e775587214a4dd872013bd4d8886ed",
					URL: "https://github.com/smol-machines/smolvm/releases/download/v1.37.0/smolvm-1.37.0-darwin-arm64.tar.gz"},
			},
		},
	}
}

// LookupToolRecipe finds a recipe by id.
func LookupToolRecipe(id string) (ToolRecipe, bool) {
	for _, r := range ToolRecipes() {
		if r.ID == id {
			return r, true
		}
	}
	return ToolRecipe{}, false
}

// ToolIDs are the recipes' ids.
func ToolIDs() []string {
	var out []string
	for _, r := range ToolRecipes() {
		out = append(out, r.ID)
	}
	return out
}

// ToolResult is what installing or removing one tool did on one machine.
type ToolResult struct {
	Machine string `json:"machine"`
	Tool    string `json:"tool"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
	// Changed: something was installed, upgraded or removed.
	Changed bool `json:"changed,omitempty"`
	// Previous is the version an upgrade replaced.
	Previous string `json:"-"`
	Path     string `json:"-"`
}
