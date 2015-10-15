package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker/distribution/digest"
	versioned "github.com/docker/distribution/manifest"
	manifest "github.com/docker/distribution/manifest/schema1"
	"github.com/docker/docker/image"
	flag "github.com/docker/docker/pkg/mflag"
	trust "github.com/docker/libtrust"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"
)

var (
	verbose, help bool
	target, key   string
)

type Layer struct {
	Id, Parent string
	BlobSum    digest.Digest
	Data       string
}

type LayerMap map[string]*Layer

func init() {
	flag.Bool([]string{"h", "-help"}, false, "Display help")
	flag.BoolVar(&verbose, []string{"v", "-verbose"}, false, "Switch to verbose output")
	flag.StringVar(&key, []string{"k", "-key-file"}, "", "Private key with which to sign")
	flag.Parse()
}

func blobSumLayer(r *tar.Reader) (digest.Digest, error) {
	sha := digest.Canonical.New()
	gw := gzip.NewWriter(sha.Hash())
	if _, err := io.Copy(gw, r); err != nil {
		return "", err
	}
	gw.Close()
	return sha.Digest(), nil
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
	var pkey trust.PrivateKey

	if key != "" {
		var err error
		pkey, err = trust.LoadKeyFile(key)
		if err != nil {
			fmt.Printf("error loading key: %s\n", err.Error())
			return
		}
	}

	if verbose {
		fmt.Errorf("signing with: %s\n", pkey.KeyID())
	}

	f, err := os.Open(target)
	if err != nil {
		fmt.Printf("error opening file: %s\n", err.Error())
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

	m := manifest.Manifest{
		Versioned: versioned.Versioned{
			SchemaVersion: 1,
		},
		Name: repo, Tag: tag, Architecture: "amd64"}

	ll := getLayersFromMap(layers)
	for _, l := range getLayersInOrder(ll) {
		m.FSLayers = append(m.FSLayers, manifest.FSLayer{BlobSum: l.BlobSum})
		m.History = append(m.History, manifest.History{V1Compatibility: l.Data})
	}

	var x []byte
	if pkey != nil {
		var sm *manifest.SignedManifest
		sm, err = manifest.Sign(&m, pkey)
		x, err = sm.MarshalJSON()
	} else {
		x, err = json.MarshalIndent(m, "", "   ")
	}
	fmt.Println(string(x))
}

func main() {
	if help {
		flag.PrintDefaults()
	} else {
		target := flag.Arg(0)
		if target != "" {
			//fmt.Printf("outputting manifest for: %q with key: %q\n", target, key)
			outputManifestFor(target)
		}
	}
}
