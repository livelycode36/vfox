/*
 *    Copyright 2023 [lihan aooohan@gmail.com]
 *
 *    Licensed under the Apache License, Version 2.0 (the "License");
 *    you may not use this file except in compliance with the License.
 *    You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 *    Unless required by applicable law or agreed to in writing, software
 *    distributed under the License is distributed on an "AS IS" BASIS,
 *    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *    See the License for the specific language governing permissions and
 *    limitations under the License.
 */

package sdk

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aooohan/version-fox/env"
	"github.com/aooohan/version-fox/util"
	"github.com/pterm/pterm"
	"github.com/schollz/progressbar/v3"
)

type Version string

type Sdk struct {
	sdkManager *Manager
	Plugin     *LuaPlugin
	// current sdk install path
	sdkRootPath string
}

func (b *Sdk) Install(version Version) error {
	success := false
	newDirPath := b.VersionPath(version)

	// Delete directory after failed installation
	defer func() {
		if !success {
			_ = os.RemoveAll(newDirPath)
		}
	}()
	label := b.label(version)
	if b.checkExists(version) {
		pterm.Printf("%s is already installed.\n", pterm.LightGreen(label))
		return fmt.Errorf("%s has been installed\n", label)
	}
	installInfo, err := b.Plugin.PreInstall(version)
	if err != nil {
		pterm.Printf("Plugin [PreInstall] error: %s\n", err.Error())
		return err
	}
	if installInfo == nil {
		pterm.Println("No information about the current version")
		return fmt.Errorf("no version")
	}
	mainSdk := installInfo.Main
	var installedSdkInfos []*Info
	path, err := b.installSdk(mainSdk, newDirPath)
	if err != nil {
		return err
	}
	installedSdkInfos = append(installedSdkInfos, &Info{
		Name:    mainSdk.Name,
		Version: mainSdk.Version,
		Path:    path,
	})
	if len(installInfo.Additional) > 0 {
		pterm.Printf("There are %d additional items that need to be installed...\n", len(installInfo.Additional))
		for _, oSdk := range installInfo.Additional {
			path, err = b.installSdk(oSdk, newDirPath)
			if err != nil {
				return err
			}
			installedSdkInfos = append(installedSdkInfos, &Info{
				Name:    oSdk.Name,
				Version: oSdk.Version,
				Path:    path,
			})
		}
	}
	success = true
	_ = b.Plugin.PostInstall(newDirPath, installedSdkInfos)
	pterm.Printf("Please use %s to use it.\n", pterm.LightBlue(fmt.Sprintf("vfox use %s", label)))
	return nil
}
func (b *Sdk) installSdk(info *Info, sdkDestPath string) (string, error) {
	pterm.Printf("Installing %s...\n", info.label())
	u, err := url.Parse(info.Path)
	label := info.label()
	if err != nil {
		return "", err
	}
	filePath, err := b.Download(u)
	if err != nil {
		fmt.Printf("Failed to download %s file, err:%s", label, err.Error())
		return "", err
	}
	defer func() {
		// del cache file
		_ = os.Remove(filePath)
	}()
	pterm.Printf("Verifying checksum %s...\n", info.Checksum.Value)
	checksum := info.Checksum.verify(filePath)
	if !checksum {
		fmt.Printf("Checksum error, file: %s\n", filePath)
		return "", errors.New("checksum error")
	}

	decompressor := util.NewDecompressor(filePath)
	if decompressor == nil {
		fmt.Printf("Unable to process current file type, file: %s\n", filePath)
		return "", fmt.Errorf("unknown file type")
	}
	pterm.Printf("Unpacking %s...\n", filePath)
	path := filepath.Join(sdkDestPath, info.Name+"-"+string(info.Version))
	err = decompressor.Decompress(path)
	if err != nil {
		fmt.Printf("Unpack failed, err:%s", err.Error())
		return "", err
	}
	pterm.Printf("Install %s success! \n", pterm.LightGreen(label))
	return path, nil
}

func (b *Sdk) Uninstall(version Version) error {
	label := b.label(version)
	if !b.checkExists(version) {
		pterm.Printf("%s is not installed...\n", pterm.Red(label))
		return fmt.Errorf("%s is not installed", label)
	}
	if b.Current() == version {
		b.clearEnvConfig(version)
	}
	path := b.VersionPath(version)
	err := os.RemoveAll(path)
	if err != nil {
		return err
	}
	pterm.Printf("Uninstalled %s successfully!\n", label)
	return nil
}

func (b *Sdk) Available() ([]*Package, error) {
	return b.Plugin.Available()
}

func (b *Sdk) Use(version Version) error {
	label := b.label(version)
	if !b.checkExists(version) {
		pterm.Printf("No %s installed, please install it first.", pterm.Yellow(label))
		return fmt.Errorf("%s is not installed", label)
	}
	sdkPackage, err := b.getLocalSdkPackage(version)
	if err != nil {
		pterm.Printf("Failed to get local sdk info, err:%s\n", err.Error())
		return err
	}
	keys, err := b.Plugin.EnvKeys(sdkPackage)
	if err != nil {
		pterm.Printf("Plugin [EnvKeys] error: err:%s\n", err.Error())
		return err
	}
	b.clearCurrentEnvConfig()
	keys = append(keys, &env.KV{
		Key:   b.envVersionKey(),
		Value: string(version),
	})
	err = b.sdkManager.EnvManager.Load(keys)
	if err != nil {
		return fmt.Errorf("Use %s error, err: %s\n", label, err)
	}
	var outputLabel string
	if len(sdkPackage.Additional) != 0 {
		var additionalLabels []string
		for _, additional := range sdkPackage.Additional {
			additionalLabels = append(additionalLabels, fmt.Sprintf("%s v%s", additional.Name, additional.Version))
		}
		outputLabel = fmt.Sprintf("%s (%s)", label, strings.Join(additionalLabels, ","))
	} else {
		outputLabel = label
	}
	pterm.Printf("Now using %s.\n", pterm.LightGreen(outputLabel))
	return b.sdkManager.EnvManager.ReShell()
}

func (b *Sdk) List() []Version {
	if !util.FileExists(b.sdkRootPath) {
		return make([]Version, 0)
	}
	var versions []Version
	dir, err := os.ReadDir(b.sdkRootPath)
	if err != nil {
		return nil
	}
	for _, d := range dir {
		if d.IsDir() && strings.HasPrefix(d.Name(), "v-") {
			versions = append(versions, Version(strings.TrimPrefix(d.Name(), "v-")))
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i] > versions[j]
	})
	return versions
}

func (b *Sdk) Current() Version {
	value, _ := b.sdkManager.EnvManager.Get(b.envVersionKey())
	return Version(value)
}

func (b *Sdk) Close() {
	b.sdkManager.EnvManager.Flush()
	b.Plugin.Close()
}
func (b *Sdk) clearCurrentEnvConfig() {
	b.clearEnvConfig(b.Current())
}

func (b *Sdk) clearEnvConfig(version Version) {
	if version == "" {
		return
	}
	sdkPackage, _ := b.getLocalSdkPackage(version)
	envKV, err := b.Plugin.EnvKeys(sdkPackage)
	if err != nil {
		return
	}
	envManager := b.sdkManager.EnvManager
	for _, kv := range envKV {
		if kv.Key == "PATH" {
			_ = envManager.Remove(kv.Value)
		} else {
			_ = envManager.Remove(kv.Key)
		}
	}
	_ = envManager.Remove(b.envVersionKey())
}

func (b *Sdk) getLocalSdkPackage(version Version) (*Package, error) {
	versionPath := b.VersionPath(version)
	mainSdk := &Info{
		Name:    b.Plugin.Name,
		Version: version,
	}
	var additional []*Info
	dir, err := os.ReadDir(versionPath)
	if err != nil {
		return nil, err
	}
	for _, d := range dir {
		if d.IsDir() {
			split := strings.Split(d.Name(), "-")
			if len(split) != 2 {
				continue
			}
			name := split[0]
			v := split[1]
			if name == b.Plugin.Name {
				mainSdk.Path = filepath.Join(versionPath, d.Name())
				continue
			}
			additional = append(additional, &Info{
				Name:    name,
				Version: Version(v),
				Path:    filepath.Join(versionPath, d.Name()),
			})
		}
	}
	if err != nil {
		return nil, err
	}
	return &Package{
		Main:       mainSdk,
		Additional: additional,
	}, nil
}

func (b *Sdk) checkExists(version Version) bool {
	return util.FileExists(b.VersionPath(version))
}

func (b *Sdk) VersionPath(version Version) string {
	return filepath.Join(b.sdkRootPath, fmt.Sprintf("v-%s", version))
}

func (b *Sdk) Download(u *url.URL) (string, error) {
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			var netErr net.Error
			if errors.As(urlErr.Err, &netErr) && netErr.Timeout() {
				return "", errors.New("request timeout")
			}
		}
		return "", err
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", errors.New("source file not found")
	}

	err = os.MkdirAll(b.sdkRootPath, 0755)
	if err != nil {
		return "", err
	}

	path := filepath.Join(b.sdkRootPath, filepath.Base(u.Path))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}

	defer f.Close()

	bar := progressbar.NewOptions64(
		resp.ContentLength,
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowBytes(true),
		progressbar.OptionFullWidth(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprintf(os.Stderr, "\n")
		}),
		progressbar.OptionSetDescription("Downloading..."),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]=[reset]",
			SaucerHead:    "[green]>[reset]",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)
	defer bar.Close()
	_, err = io.Copy(io.MultiWriter(f, bar), resp.Body)
	if err != nil {
		return "", err
	}
	return path, nil
}

func (b *Sdk) label(version Version) string {
	return fmt.Sprintf("%s@%s", strings.ToLower(b.Plugin.Name), version)
}

func (b *Sdk) envVersionKey() string {
	return fmt.Sprintf("%s_VERSION", strings.ToUpper(b.Plugin.Name))
}

func NewSdk(manager *Manager, source *LuaPlugin) (*Sdk, error) {
	name := source.Name
	return &Sdk{
		sdkManager:  manager,
		sdkRootPath: filepath.Join(manager.sdkCachePath, strings.ToLower(name)),
		Plugin:      source,
	}, nil
}
