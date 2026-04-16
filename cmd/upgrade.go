package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

const punchRepo = "ashutoshsinghai/punch"

var (
	styleGreen = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF88")).Bold(true)
	styleRed   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")).Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	styleBold  = lipgloss.NewStyle().Bold(true)
)

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade punch to the latest version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(styleDim.Render("Checking for updates..."))
		release, err := fetchRelease("latest")
		if err != nil {
			fmt.Println(styleRed.Render("Error reaching GitHub: ") + err.Error())
			os.Exit(1)
		}
		if Version != "" && Version == release.TagName {
			fmt.Println(styleGreen.Render("Already up to date ") + styleDim.Render("("+Version+")"))
			return nil
		}
		fmt.Println(styleBold.Render("Upgrading ") + styleDim.Render(Version) + styleBold.Render(" → ") + styleGreen.Render(release.TagName))
		applyRelease(release)
		fmt.Println(styleGreen.Render("Done! punch upgraded to ") + styleBold.Render(release.TagName))
		return nil
	},
}

var installCmd = &cobra.Command{
	Use:   "install [version]",
	Short: "Install a specific version of punch (e.g. v0.2.0), or --latest",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tag := args[0]
		if tag == "--latest" || tag == "latest" {
			tag = "latest"
		} else if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}

		fmt.Println(styleDim.Render("Fetching release info..."))
		release, err := fetchRelease(tag)
		if err != nil {
			fmt.Println(styleRed.Render("Error: ") + err.Error())
			os.Exit(1)
		}

		if Version != "" && Version == release.TagName {
			fmt.Println(styleGreen.Render("Already on ") + styleBold.Render(release.TagName))
			return nil
		}

		if tag == "latest" {
			fmt.Println(styleBold.Render("Installing ") + styleDim.Render(Version) + styleBold.Render(" → ") + styleGreen.Render(release.TagName))
		} else {
			fmt.Println(styleBold.Render("Installing ") + styleGreen.Render(release.TagName) + styleDim.Render(" (current: "+Version+")"))
		}

		applyRelease(release)
		fmt.Println(styleGreen.Render("Done! punch is now at ") + styleBold.Render(release.TagName))
		return nil
	},
}

// fetchRelease fetches release metadata from GitHub.
// Pass "latest" for the latest release, or a tag like "v0.2.0" for a specific one.
func fetchRelease(tag string) (githubRelease, error) {
	var url string
	if tag == "latest" {
		url = "https://api.github.com/repos/" + punchRepo + "/releases/latest"
	} else {
		url = "https://api.github.com/repos/" + punchRepo + "/releases/tags/" + tag
	}

	resp, err := http.Get(url)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return githubRelease{}, fmt.Errorf("version %q not found — check available releases at https://github.com/%s/releases", tag, punchRepo)
	}
	if resp.StatusCode != 200 {
		return githubRelease{}, fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("could not parse release info: %w", err)
	}
	return release, nil
}

// applyRelease downloads the right asset for the current platform and replaces the binary.
func applyRelease(release githubRelease) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	var assetName string
	if goos == "windows" {
		assetName = fmt.Sprintf("punch_%s_%s.zip", goos, goarch)
	} else {
		assetName = fmt.Sprintf("punch_%s_%s.tar.gz", goos, goarch)
	}

	downloadURL := ""
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		fmt.Printf("No binary found for %s/%s in release %s\n", goos, goarch, release.TagName)
		os.Exit(1)
	}

	fmt.Println(styleDim.Render("Downloading " + assetName + "..."))

	tmpDir, _ := os.MkdirTemp("", "punch-install")
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, assetName)
	if err := downloadFile(downloadURL, archivePath); err != nil {
		fmt.Printf("Download failed: %v\n", err)
		os.Exit(1)
	}

	binaryName := "punch"
	if goos == "windows" {
		binaryName = "punch.exe"
	}
	newBinaryPath := filepath.Join(tmpDir, binaryName)

	if strings.HasSuffix(archivePath, ".tar.gz") {
		if err := extractTarGz(archivePath, binaryName, newBinaryPath); err != nil {
			fmt.Printf("Extraction failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := extractZip(archivePath, binaryName, newBinaryPath); err != nil {
			fmt.Printf("Extraction failed: %v\n", err)
			os.Exit(1)
		}
	}

	currentBinary, err := os.Executable()
	if err != nil {
		fmt.Printf("Could not find current binary path: %v\n", err)
		os.Exit(1)
	}
	currentBinary, _ = filepath.EvalSymlinks(currentBinary)

	tmpBinary := currentBinary + ".new"
	if err := copyFile(newBinaryPath, tmpBinary); err != nil {
		fmt.Printf("Could not write new binary: %v\n", err)
		os.Exit(1)
	}
	os.Chmod(tmpBinary, 0755)

	if err := os.Rename(tmpBinary, currentBinary); err != nil {
		fmt.Printf("Could not replace binary (try with sudo): %v\n", err)
		os.Remove(tmpBinary)
		os.Exit(1)
	}
}

// downloadFile downloads a URL to a local file.
func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
