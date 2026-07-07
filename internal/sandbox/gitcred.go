package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// gitTokenEnvVar is the env var the generated credential helper reads the
// token from. Keeping the token in env (not written to .gitconfig) means
// no secret lands in a file, even an ephemeral one.
const gitTokenEnvVar = "APE_GIT_TOKEN" //nolint:gosec // G101: this is an env var name, not a credential value

// githubKnownHosts pins GitHub's published SSH host keys so deploy-key /
// agent modes get no TOFU prompt inside the headless guest. These are
// public keys; verify against https://api.github.com/meta ("ssh_keys")
// when cutting a release — GitHub rotates them rarely but not never.
const githubKnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
`

// composeGit writes the git-credential files described by the profile's
// git.mode and records any binds the runner must add. Nothing from the
// real ~/.ssh or ~/.gitconfig is ever used — every artifact is authored
// per job (PLAN-16 D5).
func composeGit(opts ComposeOptions, comp *Composition) error {
	switch opts.Profile.Git.Mode {
	case GitNone, "":
		return nil
	case GitToken:
		return composeGitToken(opts, comp)
	case GitDeployKey:
		return composeGitDeployKey(opts, comp)
	case GitAgent:
		return composeGitAgent(opts, comp)
	default:
		return fmt.Errorf("compose git: unknown mode %q", opts.Profile.Git.Mode)
	}
}

// composeGitToken writes a ~/.gitconfig wiring a credential helper that
// serves the env token over https://github.com, plus url.insteadOf so
// SSH-style remotes in existing checkouts keep working. The token itself
// rides env, never a file.
func composeGitToken(opts ComposeOptions, comp *Composition) error {
	token, err := ResolveSecret(opts.Profile.Git.TokenSource)
	if err != nil {
		return fmt.Errorf("compose git token: %w", err)
	}
	// The helper prints the standard git-credential key/value reply. A PAT
	// works with any username; x-access-token is the GitHub App convention.
	gitconfig := `[credential "https://github.com"]
	helper = "!f() { echo username=x-access-token; echo \"password=$` + gitTokenEnvVar + `\"; }; f"
[url "https://github.com/"]
	insteadOf = git@github.com:
	insteadOf = ssh://git@github.com/
`
	if err := os.WriteFile(filepath.Join(opts.StagingDir, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		return fmt.Errorf("compose git token: write .gitconfig: %w", err)
	}
	comp.Env = append(comp.Env, gitTokenEnvVar+"="+token)
	return nil
}

// composeGitDeployKey stages an .ssh dir with a pinned known_hosts and an
// ssh config, and records a read-only bind of the host deploy key at
// ~/.ssh/id_ed25519. The real ~/.ssh is never touched.
func composeGitDeployKey(opts ComposeOptions, comp *Composition) error {
	keyPath := opts.Profile.Git.DeployKey
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("compose git deploy-key: key not found at %s: %w", keyPath, err)
	}
	if err := stageSSHDir(opts, sshConfigIdentity); err != nil {
		return err
	}
	comp.Binds = append(comp.Binds, BindMount{
		Source:   keyPath,
		Dest:     filepath.Join(comp.GuestHome, ".ssh", "id_ed25519"),
		ReadOnly: true,
	})
	return nil
}

// composeGitAgent stages the .ssh dir and records a bind of the host
// ssh-agent socket. Keys never enter the guest — only signing capability
// for the job's duration (see the D6 caveat). SSH_AUTH_SOCK must be set
// on the host; the runner rewrites the env var to the in-guest path.
func composeGitAgent(opts ComposeOptions, comp *Composition) error {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return errors.New("compose git agent: SSH_AUTH_SOCK is not set on the host")
	}
	if err := stageSSHDir(opts, sshConfigAgent); err != nil {
		return err
	}
	guestSock := filepath.Join(comp.GuestHome, ".ssh", "agent.sock")
	comp.Binds = append(comp.Binds, BindMount{Source: sock, Dest: guestSock, ReadOnly: false})
	comp.Env = append(comp.Env, "SSH_AUTH_SOCK="+guestSock)
	return nil
}

const (
	sshConfigIdentity = "Host github.com\n\tHostName github.com\n\tUser git\n\tIdentityFile ~/.ssh/id_ed25519\n\tIdentitiesOnly yes\n\tStrictHostKeyChecking yes\n\tUserKnownHostsFile ~/.ssh/known_hosts\n"
	sshConfigAgent    = "Host github.com\n\tHostName github.com\n\tUser git\n\tStrictHostKeyChecking yes\n\tUserKnownHostsFile ~/.ssh/known_hosts\n"
)

// stageSSHDir creates <staging>/.ssh with a pinned known_hosts and the
// given ssh config, both 0600 in a 0700 dir (ssh refuses loose perms).
func stageSSHDir(opts ComposeOptions, sshConfig string) error {
	sshDir := filepath.Join(opts.StagingDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("compose git: mkdir .ssh: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(githubKnownHosts), 0o600); err != nil {
		return fmt.Errorf("compose git: write known_hosts: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(sshConfig), 0o600); err != nil {
		return fmt.Errorf("compose git: write ssh config: %w", err)
	}
	return nil
}
