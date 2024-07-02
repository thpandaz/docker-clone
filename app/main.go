package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type DockerTokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

type DockerLayer struct {
	Digest string `json:"digest"`
}

type DockerManifestResponse struct {
	SchemaVersion int           `json:"schemaVersion"`
	Name          string        `json:"name"`
	Tag           string        `json:"tag"`
	Layers        []DockerLayer `json:"layers"`
}

// Helper function to handle errors
func must(err error) {
	if err != nil {
		panic(err)
	}
}

func fetchDockerRegistryToken(repository string) (DockerTokenResponse, error) {
	var token DockerTokenResponse
	res, err := http.Get(fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull", repository))
	if err != nil {
		log.Fatalln(err)
		return token, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&token); err != nil {
		log.Fatalln(err)
		return token, err
	}
	return token, nil
}

func fetchDockerManifest(repository, tag, token string) (DockerManifestResponse, error) {
	var manifest DockerManifestResponse
	req, err := http.NewRequest("GET", fmt.Sprintf("https://registry-1.docker.io/v2/%s/manifests/%s", repository, tag), nil)
	if err != nil {
		log.Fatalln(err)
		return manifest, err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		log.Fatalln(err)
		return manifest, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&manifest); err != nil {
		log.Fatalln(err)
		return manifest, err
	}
	return manifest, nil
}

func downloadAndExtractLayer(dir string, repository string, layer DockerLayer, token string) error {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/%s", repository, layer.Digest), nil)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	defer resp.Body.Close()
	filePath := filepath.Join(dir, layer.Digest+".tar")
	file, err := os.Create(filePath)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	cmd := exec.Command("tar", "-xvf", filePath, "-C", dir)
	err = cmd.Run()
	if err != nil {
		log.Fatalln(err)
		return err
	}
	// Remove the tar file after extraction
	err = os.Remove(filePath)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	return nil
}

func pullDockerImage(dir, image string) error {
	token, err := fetchDockerRegistryToken("library/" + image)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	tag := "latest"
	if strings.Contains(image, ":") {
		parts := strings.Split(image, ":")
		image = parts[0]
		tag = parts[1]
	}
	manifest, err := fetchDockerManifest("library/"+image, tag, token.Token)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	for _, layer := range manifest.Layers {
		err := downloadAndExtractLayer(dir, "library/"+image, layer, token.Token)
		if err != nil {
			fmt.Println("Error downloading layer:", err)
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: your_docker.sh run <image> <command> <arg1> <arg2> ...")
		os.Exit(1)
	}
	image := os.Args[2]
	command := os.Args[3]
	args := os.Args[4:len(os.Args)]

	sandboxDir, err := os.MkdirTemp("", "chroot")
	if err != nil {
		fmt.Printf("Err MkdirTemp: %v", err)
		os.Exit(1)
	}
	defer os.RemoveAll(sandboxDir)

	err = pullDockerImage(sandboxDir, image)
	if err != nil {
		fmt.Printf("Err on pulling image: %v", err)
		os.Exit(1)
	}

	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("mkdir -p %s/usr/local/bin && cp /usr/local/bin/docker-explorer %s/usr/local/bin/docker-explorer", sandboxDir, sandboxDir))
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Err on setting up sandbox: %v", err)
		os.Exit(1)
	}

	if err := syscall.Chroot(sandboxDir); err != nil {
		fmt.Printf("Err Chroot: %v", err)
		os.Exit(1)
	}

	cmd = exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID,
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Err: %v", err)
		os.Exit(cmd.ProcessState.ExitCode())
	}
}
