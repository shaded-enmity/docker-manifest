package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker/docker/image"
	flag "github.com/docker/docker/pkg/mflag"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"
)

var (
	verbose, help bool
	target        string
)

type BlobSum struct {
	Sum string `json:"blobSum"`
}

type Compat struct {
	Data string `json:"v1Compatibility"`
}

type Layer struct {
	Id, Parent string
	BlobSum    string
	Data       string
}

type LayerMap map[string]*Layer

type Manifest struct {
	Version      int       `json:"schemaVersion"`
	Name         string    `json:"name"`
	Tag          string    `json:"tag"`
	Architecture string    `json:"architecture"`
	Layers       []BlobSum `json:"fsLayers"`
	History      []Compat  `json:"history"`
}

func init() {
	flag.Bool([]string{"-h", "--help"}, false, "Display help")
	flag.BoolVar(&verbose, []string{"-v", "--verbose"}, false, "Switch to verbose output")
	flag.Parse()
}

func blobSumLayer(r *tar.Reader) (string, error) {
	sha := sha256.New()
	gw := gzip.NewWriter(sha)
	if _, err := io.Copy(gw, r); err != nil {
		return "", err
	}
	gw.Close()
	return hex.EncodeToString(sha.Sum(nil)), nil
}

func getLayerPrefix(s string) string {
	_, b := path.Split(path.Dir(s))
	return path.Clean(b)
}

func getLayerInfo(b []byte) (string, string, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return "", "", err
	}
	if raw["parent"] == nil {
		return "", raw["id"].(string), nil
	}
	return raw["parent"].(string), raw["id"].(string), nil
}

func getLayersFromMap(lm LayerMap) []*Layer {
	out := make([]*Layer, 0, len(lm))
	for _, v := range lm {
		out = append(out, v)
	}
	return out
}

func findChild(id string, layers []*Layer) *Layer {
	for _, l := range layers {
		if l.Parent == id {
			return l
		}
	}
	return nil
}

func getLayersInOrder(layers []*Layer) []*Layer {
	root := findChild("", layers)

	if root == nil {
		panic(errors.New("Unable to find root layer"))
	}

	out := make([]*Layer, 0, len(layers))
	out = append(out, root)
	for {
		root = findChild(root.Id, layers)
		if root == nil {
			break
		}
		out = append(out, root)
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	return out
}

func getRepoInfo(ri map[string]interface{}) (string, string) {
	var (
		repo string
		tag  string
	)

	for k, v := range ri {
		repo = k
		for vv, _ := range v.(map[string]interface{}) {
			tag = vv
		}
	}

	return repo, tag
}

func outputManifestFor(target string) {
	f, err := os.Open(target)
	if err != nil {
		return
	}

	defer func() {
		if err := f.Close(); err != nil {
			panic(err)
		}
	}()

	var (
		repo, tag string
	)
	layers := LayerMap{}
	t := tar.NewReader(bufio.NewReader(f))
	for {
		hdr, err := t.Next()
		if err == io.EOF {
			break
		}

		if strings.HasSuffix(hdr.Name, "layer.tar") {
			id := getLayerPrefix(hdr.Name)
			sum, _ := blobSumLayer(t)
			if _, ok := layers[id]; !ok {
				layers[id] = &Layer{Id: id}
			} else {
				layers[id].BlobSum = sum
			}
		}

		if strings.HasSuffix(hdr.Name, "json") {
			data, _ := ioutil.ReadAll(t)
			parent, id, _ := getLayerInfo(data)
			if _, ok := layers[id]; !ok {
				layers[id] = &Layer{Id: id, Parent: parent}
			} else {
				layers[id].Parent = parent
			}

			var img image.Image
			json.Unmarshal(data, &img)
			b, _ := json.Marshal(img)
			layers[id].Data = string(b) + "\n"
		}

		if hdr.Name == "repositories" {
			r, _ := ioutil.ReadAll(t)
			var raw map[string]interface{}
			if err := json.Unmarshal(r, &raw); err != nil {
				return
			}

			repo, tag = getRepoInfo(raw)
			if !strings.Contains(repo, "/") {
				repo = "library/" + repo
			}
		}
	}

	m := Manifest{Name: repo, Tag: tag, Architecture: "amd64", Version: 1}
	ll := getLayersFromMap(layers)
	for _, l := range getLayersInOrder(ll) {
		m.Layers = append(m.Layers, BlobSum{Sum: "sha256:" + l.BlobSum})
		m.History = append(m.History, Compat{Data: l.Data})
	}
	x, _ := json.MarshalIndent(m, "", "   ")
	fmt.Println(string(x))
}

func main() {
	if help {
		flag.PrintDefaults()
	} else {
		target := flag.Arg(0)
		if target != "" {
			//fmt.Printf("outputting manifest for: %q\n", target)
			outputManifestFor(target)
		}
	}
}
