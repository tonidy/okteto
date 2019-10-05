package model

import (
	"errors"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/okteto/okteto/pkg/log"
	yaml "gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	oktetoStatefulSetTemplate = "okteto-%s"
	oktetoVolumeName          = "okteto"
	//OktetoAutoCreateAnnotation indicates if the deployment was auto generatted by okteto up
	OktetoAutoCreateAnnotation = "dev.okteto.com/auto-create"

	//OktetoInitContainer name of the okteto init container
	OktetoInitContainer = "okteto-init"

	//DefaultImage default image for sandboxes
	DefaultImage = "okteto/desk:latest"

	//TranslationVersion version of the translation schema
	TranslationVersion = "1.0"
)

var (
	errBadName = fmt.Errorf("Invalid name: must consist of lower case alphanumeric characters or '-', and must start and end with an alphanumeric character")

	// ValidKubeNameRegex is the regex to validate a kubernetes resource name
	ValidKubeNameRegex = regexp.MustCompile(`[^a-z0-9\-]+`)

	devReplicas                      int32 = 1
	devTerminationGracePeriodSeconds int64
)

//Dev represents a cloud native development environment
type Dev struct {
	Name            string               `json:"name" yaml:"name"`
	Labels          map[string]string    `json:"labels,omitempty" yaml:"labels,omitempty"`
	Namespace       string               `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Container       string               `json:"container,omitempty" yaml:"container,omitempty"`
	Image           string               `json:"image,omitempty" yaml:"image,omitempty"`
	ImagePullPolicy apiv1.PullPolicy     `json:"imagePullPolicy,omitempty" yaml:"imagePullPolicy,omitempty"`
	Environment     []EnvVar             `json:"environment,omitempty" yaml:"environment,omitempty"`
	Command         []string             `json:"command,omitempty" yaml:"command,omitempty"`
	WorkDir         string               `json:"workdir,omitempty" yaml:"workdir,omitempty"`
	MountPath       string               `json:"mountpath,omitempty" yaml:"mountpath,omitempty"`
	SubPath         string               `json:"subpath,omitempty" yaml:"subpath,omitempty"`
	Volumes         []string             `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	SecurityContext *SecurityContext     `json:"securityContext,omitempty" yaml:"securityContext,omitempty"`
	Forward         []Forward            `json:"forward,omitempty" yaml:"forward,omitempty"`
	Resources       ResourceRequirements `json:"resources,omitempty" yaml:"resources,omitempty"`
	DevPath         string               `json:"-" yaml:"-"`
	DevDir          string               `json:"-" yaml:"-"`
	Services        []*Dev               `json:"services,omitempty" yaml:"services,omitempty"`
}

// SecurityContext represents a pod security context
type SecurityContext struct {
	RunAsUser    *int64        `json:"runAsUser,omitempty" yaml:"runAsUser,omitempty"`
	RunAsGroup   *int64        `json:"runAsGroup,omitempty" yaml:"runAsGroup,omitempty"`
	FSGroup      *int64        `json:"fsGroup,omitempty" yaml:"fsGroup,omitempty"`
	Capabilities *Capabilities `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// Capabilities sets the linux capabilities of a container
type Capabilities struct {
	Add  []apiv1.Capability `json:"add,omitempty" yaml:"add,omitempty"`
	Drop []apiv1.Capability `json:"drop,omitempty" yaml:"drop,omitempty"`
}

// EnvVar represents an environment value. When loaded, it will expand from the current env
type EnvVar struct {
	Name  string
	Value string
}

// Forward represents a port forwarding definition
type Forward struct {
	Local  int
	Remote int
}

// ResourceRequirements describes the compute resource requirements.
type ResourceRequirements struct {
	Limits   ResourceList
	Requests ResourceList
}

// ResourceList is a set of (resource name, quantity) pairs.
type ResourceList map[apiv1.ResourceName]resource.Quantity

//Get returns a Dev object from a given file
func Get(devPath string) (*Dev, error) {
	b, err := ioutil.ReadFile(devPath)
	if err != nil {
		return nil, err
	}

	dev, err := Read(b)
	if err != nil {
		return nil, err
	}

	if err := dev.validate(); err != nil {
		return nil, err
	}

	dev.DevDir, err = filepath.Abs(filepath.Dir(devPath))
	if err != nil {
		return nil, err
	}
	dev.DevPath = filepath.Base(devPath)

	return dev, nil
}

//Read reads an okteto manifests
func Read(bytes []byte) (*Dev, error) {
	dev := &Dev{
		Environment: make([]EnvVar, 0),
		Command:     make([]string, 0),
		Forward:     make([]Forward, 0),
		Volumes:     make([]string, 0),
		Resources: ResourceRequirements{
			Limits:   ResourceList{},
			Requests: ResourceList{},
		},
		Services: make([]*Dev, 0),
	}
	if err := yaml.UnmarshalStrict(bytes, dev); err != nil {
		if strings.HasPrefix(err.Error(), "yaml: unmarshal errors:") {
			var sb strings.Builder
			sb.WriteString("Invalid manifest:\n")
			l := strings.Split(err.Error(), "\n")
			for i := 1; i < len(l); i++ {
				e := strings.TrimSuffix(l[i], "in type model.Dev")
				e = strings.TrimSpace(e)
				sb.WriteString(fmt.Sprintf("    - %s\n", e))
			}

			sb.WriteString("    See https://okteto.com/docs/reference/manifest for details")
			return nil, errors.New(sb.String())
		}
		msg := strings.Replace(err.Error(), "yaml: unmarshal errors:", "invalid manifest:", 1)
		msg = strings.TrimSuffix(msg, "in type model.Dev")
		return nil, errors.New(msg)
	}
	if err := dev.setDefaults(); err != nil {
		return nil, err
	}
	return dev, nil
}

func (dev *Dev) setDefaults() error {
	if len(dev.Command) == 0 {
		dev.Command = []string{"sh"}
	}
	if dev.MountPath == "" && dev.WorkDir == "" {
		dev.MountPath = "/okteto"
		dev.WorkDir = "/okteto"
	}
	if dev.ImagePullPolicy == "" {
		dev.ImagePullPolicy = apiv1.PullAlways
	}
	if dev.WorkDir != "" && dev.MountPath == "" {
		dev.MountPath = dev.WorkDir
	}
	if dev.Labels == nil {
		dev.Labels = map[string]string{}
	}
	dev.Volumes = uniqueStrings(dev.Volumes)
	for _, s := range dev.Services {
		if s.MountPath == "" && s.WorkDir == "" {
			s.MountPath = "/okteto"
			s.WorkDir = "/okteto"
		}
		if s.ImagePullPolicy == "" {
			s.ImagePullPolicy = apiv1.PullAlways
		}
		if s.WorkDir != "" && s.MountPath == "" {
			s.MountPath = s.WorkDir
		}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		if s.Name != "" && len(s.Labels) > 0 {
			return fmt.Errorf("'name' and 'labels' cannot be defined at the same time for service '%s'", s.Name)
		}
		s.Namespace = ""
		s.Forward = make([]Forward, 0)
		s.Volumes = make([]string, 0)
		s.Services = make([]*Dev, 0)
		s.Resources = ResourceRequirements{
			Limits:   ResourceList{},
			Requests: ResourceList{},
		}
	}
	return nil
}

func (dev *Dev) validate() error {
	if dev.Name == "" {
		return fmt.Errorf("Name cannot be empty")
	}

	if ValidKubeNameRegex.MatchString(dev.Name) {
		return errBadName
	}

	if strings.HasPrefix(dev.Name, "-") || strings.HasSuffix(dev.Name, "-") {
		return errBadName
	}

	if err := validatePullPolicy(dev.ImagePullPolicy); err != nil {
		return err
	}

	for _, s := range dev.Services {
		if err := validatePullPolicy(s.ImagePullPolicy); err != nil {
			return err
		}
	}

	return nil
}

func validatePullPolicy(pullPolicy apiv1.PullPolicy) error {
	switch pullPolicy {
	case apiv1.PullAlways:
	case apiv1.PullIfNotPresent:
	case apiv1.PullNever:
	default:
		return fmt.Errorf("supported values for 'imagePullPolicy' are: 'Always', 'IfNotPresent' or 'Never'")
	}
	return nil
}

//LoadRemote configures remote execution
func (dev *Dev) LoadRemote(localPort int) {
	dev.Command = []string{"/var/okteto/bin/remote"}
	dev.Forward = append(
		dev.Forward,
		Forward{
			Local:  localPort,
			Remote: 22000,
		},
	)

	dev.Volumes = append(dev.Volumes, "/root/.vscode-server")
	dev.Volumes = uniqueStrings(dev.Volumes)

	if dev.SecurityContext == nil {
		dev.SecurityContext = &SecurityContext{}
	}
	if dev.SecurityContext.Capabilities == nil {
		dev.SecurityContext.Capabilities = &Capabilities{}
	}
	if dev.SecurityContext.Capabilities.Add == nil {
		dev.SecurityContext.Capabilities.Add = []apiv1.Capability{}
	}

	dev.SecurityContext.Capabilities.Add = append(dev.SecurityContext.Capabilities.Add, "SYS_PTRACE")
	log.Infof("enabled remote mode")
}

//GetStatefulSetName returns the syncthing statefulset name for a given dev environment
func (dev *Dev) GetStatefulSetName() string {
	n := fmt.Sprintf(oktetoStatefulSetTemplate, dev.Name)
	if len(n) > 52 {
		n = n[0:52]
	}
	return n
}

// LabelsSelector returns the labels of a Deployment as a k8s selector
func (dev *Dev) LabelsSelector() string {
	labels := ""
	for k := range dev.Labels {
		if labels == "" {
			labels = fmt.Sprintf("%s=%s", k, dev.Labels[k])
		} else {
			labels = fmt.Sprintf("%s, %s=%s", labels, k, dev.Labels[k])
		}
	}
	return labels
}

//FullSubPath returns the full subpath in the okteto volume
func (dev *Dev) FullSubPath(i int, subPath string) string {
	if dev.SubPath == "" {
		return filepath.Join(dev.Name, fmt.Sprintf("data-%d", i))
	}
	return filepath.Join(dev.Name, fmt.Sprintf("data-%d", i), dev.SubPath)
}

// ToTranslationRule translates a dev struct into a translation rule
func (dev *Dev) ToTranslationRule(main *Dev, d *appsv1.Deployment) *TranslationRule {
	rule := &TranslationRule{
		Container:       dev.Container,
		Image:           dev.Image,
		ImagePullPolicy: dev.ImagePullPolicy,
		Environment:     dev.Environment,
		WorkDir:         dev.WorkDir,
		Volumes: []VolumeMount{
			VolumeMount{
				Name:      oktetoVolumeName,
				MountPath: dev.MountPath,
				SubPath:   main.FullSubPath(0, dev.SubPath),
			},
		},
		SecurityContext: dev.SecurityContext,
		Resources:       dev.Resources,
	}

	if main == dev {
		rule.Healthchecks = false
		rule.Command = []string{"tail"}
		rule.Args = []string{"-f", "/dev/null"}
	} else {
		rule.Healthchecks = true
		if len(dev.Command) > 0 {
			rule.Command = dev.Command
			rule.Args = []string{}
		}
	}

	for i, v := range dev.Volumes {
		rule.Volumes = append(
			rule.Volumes,
			VolumeMount{
				Name:      oktetoVolumeName,
				MountPath: v,
				SubPath:   main.FullSubPath(i+1, main.SubPath),
			},
		)
	}
	return rule
}

//GevSandbox returns a deployment sandbox
func (dev *Dev) GevSandbox() *appsv1.Deployment {
	if dev.Image == "" {
		dev.Image = DefaultImage
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dev.Name,
			Namespace: dev.Namespace,
			Annotations: map[string]string{
				OktetoAutoCreateAnnotation: "true",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &devReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": dev.Name,
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": dev.Name,
					},
				},
				Spec: apiv1.PodSpec{
					TerminationGracePeriodSeconds: &devTerminationGracePeriodSeconds,
					Containers: []apiv1.Container{
						apiv1.Container{
							Name:            "dev",
							Image:           dev.Image,
							ImagePullPolicy: apiv1.PullAlways,
							Command:         []string{"tail"},
							Args:            []string{"-f", "/dev/null"}},
					},
				},
			},
		},
	}
}
