/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package git provides a client to plugins that can do git operations.
package git

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

const github = "https://github.com"

// Client can clone repos. It keeps a local cache, so successive clones of the
// same repo should be quick. Create with NewClient. Be sure to clean it up.
type Client struct {
	// dir is the location of the git cache.
	dir string
	// git is the path to the git binary.
	git string
	// base is the base path for git clone calls. For users it will be set to
	// GitHub, but for tests set it to a directory with git repos.
	base string

	// The mutex protects repoLocks which protect individual repos. This is
	// necessary because Clone calls for the same repo are racy. Rather than
	// one lock for all repos, use a lock per repo.
	// Lock with Client.lockRepo, unlock with Client.unlockRepo.
	rlm       sync.Mutex
	repoLocks map[string]*sync.Mutex
}

// Clean removes the local repo cache. The Client is unusable after calling.
func (c *Client) Clean() error {
	return os.RemoveAll(c.dir)
}

// NewClient returns a client that talks to GitHub. It will fail if git is not
// in the PATH.
func NewClient() (*Client, error) {
	g, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	t, err := ioutil.TempDir("", "git")
	if err != nil {
		return nil, err
	}
	return &Client{
		dir:       t,
		git:       g,
		base:      github,
		repoLocks: make(map[string]*sync.Mutex),
	}, nil
}

// SetRemote sets the remote for the client. This is not thread-safe, and is
// useful for testing. The client will clone from remote/org/repo, and Repo
// objects spun out of the client will also hit that path.
func (c *Client) SetRemote(remote string) {
	c.base = remote
}

func (c *Client) lockRepo(repo string) {
	c.rlm.Lock()
	if _, ok := c.repoLocks[repo]; !ok {
		c.repoLocks[repo] = &sync.Mutex{}
	}
	m := c.repoLocks[repo]
	c.rlm.Unlock()
	m.Lock()
}

func (c *Client) unlockRepo(repo string) {
	c.rlm.Lock()
	defer c.rlm.Unlock()
	c.repoLocks[repo].Unlock()
}

// Clone clones a repository. Pass the full repository name, such as
// "kubernetes/test-infra" as the repo.
// This function may take a long time if it is the first time cloning the repo.
// In that case, it must do a full git mirror clone. For large repos, this can
// take a while. Once that is done, it will do a git fetch instead of a clone,
// which will usually take at most a few seconds.
func (c *Client) Clone(repo string) (*Repo, error) {
	c.lockRepo(repo)
	defer c.unlockRepo(repo)

	remote := c.base + "/" + repo
	cache := filepath.Join(c.dir, repo) + ".git"
	if _, err := os.Stat(cache); os.IsNotExist(err) {
		// Cache miss, clone it now.
		if err := os.Mkdir(filepath.Dir(cache), os.ModePerm); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if b, err := exec.Command(c.git, "clone", "--mirror", remote, cache).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git cache clone error: %v. output: %s", err, string(b))
		}
	} else if err != nil {
		return nil, err
	} else {
		// Cache hit. Do a git fetch to keep updated.
		cmd := exec.Command(c.git, "fetch")
		cmd.Dir = cache
		if b, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git fetch error: %v. output: %s", err, string(b))
		}
	}
	t, err := ioutil.TempDir("", "git")
	if err != nil {
		return nil, err
	}
	if b, err := exec.Command(c.git, "clone", cache, t).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git repo clone error: %v. output: %s", err, string(b))
	}
	return &Repo{
		Dir:  t,
		git:  c.git,
		base: c.base,
		repo: repo,
	}, nil
}

// Repo is a clone of a git repository. Create with Client.Clone, and don't
// forget to clean it up after.
type Repo struct {
	// Dir is the location of the git repo.
	Dir string

	// git is the path to the git binary.
	git string
	// base is the base path for remote git fetch calls.
	base string
	// repo is the full repo name: "org/repo".
	repo string
}

// Clean deletes the repo. It is unusable after calling.
func (r *Repo) Clean() error {
	return os.RemoveAll(r.Dir)
}

func (r *Repo) gitCommand(arg ...string) *exec.Cmd {
	cmd := exec.Command(r.git, arg...)
	cmd.Dir = r.Dir
	return cmd
}

// CheckoutPullRequest does exactly that.
func (r *Repo) CheckoutPullRequest(number int) error {
	fetch := r.gitCommand("fetch", r.base+"/"+r.repo, fmt.Sprintf("pull/%d/head:pull%d", number, number))
	if b, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed for PR %d: %v. output: %s", number, err, string(b))
	}
	co := r.gitCommand("checkout", fmt.Sprintf("pull%d", number))
	if b, err := co.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout failed for PR %d: %v. output: %s", number, err, string(b))
	}
	return nil
}
