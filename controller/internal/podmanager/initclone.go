package podmanager

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
// This prevents shell injection when interpolating values into shell scripts.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildInitCloneContainer creates an init container that clones the project's repo
// into the workspace from GitURL, plus any reference repos.
// Returns nil if no clone source is configured.
func (m *K8sManager) buildInitCloneContainer(spec AgentPodSpec) *corev1.Container {
	if spec.GitURL == "" && len(spec.ReferenceRepos) == 0 {
		return nil
	}

	branch := spec.GitDefaultBranch
	if branch == "" {
		branch = "main"
	}

	script := "set -e\napk add --no-cache git\n"

	// Clone primary repo if configured.
	// All user-controlled values are shell-quoted to prevent injection.
	if spec.GitURL != "" {
		qProject := shellQuote(spec.Project)
		qBranch := shellQuote(branch)
		qGitURL := shellQuote(spec.GitURL)
		qAgent := shellQuote(spec.AgentName)
		qAgentEmail := shellQuote(spec.AgentName + "@gasboat")

		script += fmt.Sprintf(`git config --global --add safe.directory %s/%s/work
WORK_DIR=%s/%s/work
if [ -d "$WORK_DIR/.git" ]; then
  echo "Repo already cloned, fetching updates..."
  cd "$WORK_DIR"
  git fetch --all --prune
  git checkout %s
  git pull --ff-only || true
else
  echo "Cloning..."
  mkdir -p "$(dirname "$WORK_DIR")"
  git clone -b %s %s "$WORK_DIR"
  cd "$WORK_DIR"
fi
`, MountWorkspace, qProject, MountWorkspace, qProject, qBranch, qBranch, qGitURL)

		// Configure git identity from agent env vars.
		script += fmt.Sprintf("git config user.name %s\ngit config user.email %s\n", qAgent, qAgentEmail)
	}

	// Clone reference repos.
	// All user-controlled values are shell-quoted to prevent injection.
	for _, ref := range spec.ReferenceRepos {
		refBranch := ref.Branch
		if refBranch == "" {
			refBranch = "main"
		}
		qRefDir := shellQuote(fmt.Sprintf("%s/%s/repos/%s", MountWorkspace, spec.Project, ref.Name))
		qRefBranch := shellQuote(refBranch)
		qRefName := shellQuote(ref.Name)
		qRefURL := shellQuote(ref.URL)
		script += fmt.Sprintf(`
REFDIR=%s
if [ -d "$REFDIR/.git" ]; then
  echo "Reference repo" %s "already present, updating..."
  cd "$REFDIR" && git fetch --all --prune && git checkout %s && git pull --ff-only || true
else
  echo "Cloning reference repo" %s "..."
  mkdir -p "$(dirname "$REFDIR")"
  git clone --depth 1 -b %s %s "$REFDIR"
fi
`, qRefDir, qRefName, qRefBranch, qRefName, qRefBranch, qRefURL)
	}

	// Run init container as root so apk can install git, then chown
	// the workspace to the agent UID/GID so the main container can write.
	script += fmt.Sprintf("chown -R %d:%d %s\n", AgentUID, AgentGID, shellQuote(fmt.Sprintf("%s/%s", MountWorkspace, spec.Project)))

	// Insert git credential helper setup after apk installs git.
	// Must come after "apk add" so git binary is available for git config.
	if spec.GitCredentialsSecret != "" || spec.GitlabTokenSecret != "" {
		credSetup := `# Configure git credentials for private repos
git config --global credential.helper 'store --file=/tmp/.git-credentials'
touch /tmp/.git-credentials
if [ -n "$GIT_USERNAME" ] && [ -n "$GIT_TOKEN" ]; then
  printf "https://${GIT_USERNAME}:${GIT_TOKEN}@github.com\n" >> /tmp/.git-credentials
fi
if [ -n "$GITLAB_TOKEN" ]; then
  printf "https://oauth2:${GITLAB_TOKEN}@gitlab.com\n" >> /tmp/.git-credentials
elif [ -n "$GIT_USERNAME" ] && [ -n "$GIT_TOKEN" ]; then
  printf "https://${GIT_USERNAME}:${GIT_TOKEN}@gitlab.com\n" >> /tmp/.git-credentials
fi
`
		script = strings.Replace(script, "apk add --no-cache git\n", "apk add --no-cache git\n"+credSetup, 1)
	}

	// Build env vars for the init container.
	var initEnv []corev1.EnvVar
	if spec.GitCredentialsSecret != "" {
		initEnv = append(initEnv,
			corev1.EnvVar{
				Name: "GIT_USERNAME",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.GitCredentialsSecret},
						Key:                  "username",
					},
				},
			},
			corev1.EnvVar{
				Name: "GIT_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.GitCredentialsSecret},
						Key:                  "token",
					},
				},
			},
		)
	}
	if spec.GitlabTokenSecret != "" {
		initEnv = append(initEnv,
			corev1.EnvVar{
				Name: "GITLAB_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.GitlabTokenSecret},
						Key:                  "token",
					},
				},
			},
		)
	}

	runAsRoot := int64(0)
	runAsNonRoot := false
	return &corev1.Container{
		Name:            InitCloneName,
		Image:           InitCloneImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/bin/sh", "-c", script},
		Env:             initEnv,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:    &runAsRoot,
			RunAsNonRoot: &runAsNonRoot,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: VolumeWorkspace, MountPath: MountWorkspace},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
}
