package deployments

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/okteto/okteto/pkg/errors"
	"github.com/okteto/okteto/pkg/log"
	"github.com/okteto/okteto/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

//Get returns a deployment object given its name and namespace
func Get(dev *model.Dev, namespace string, c *kubernetes.Clientset) (*appsv1.Deployment, error) {
	if namespace == "" {
		return nil, fmt.Errorf("empty namespace")
	}

	var d *appsv1.Deployment
	var err error

	if len(dev.Labels) == 0 {
		d, err = c.AppsV1().Deployments(namespace).Get(dev.Name, metav1.GetOptions{})
		if err != nil {
			log.Debugf("error while retrieving deployment %s/%s: %s", namespace, dev.Name, err)
			return nil, err
		}
	} else {
		deploys, err := c.AppsV1().Deployments(namespace).List(
			metav1.ListOptions{
				LabelSelector: dev.LabelsSelector(),
			},
		)
		if err != nil {
			return nil, err
		}
		if len(deploys.Items) == 0 {
			return nil, errors.ErrNotFound
		}
		if len(deploys.Items) > 1 {
			return nil, fmt.Errorf("Found '%d' deployments instead of 1", len(deploys.Items))
		}
		d = &deploys.Items[0]
	}

	return d, nil
}

//GetTranslations fills all the deployments pointed by a dev environment
func GetTranslations(dev *model.Dev, d *appsv1.Deployment, c *kubernetes.Clientset) (map[string]*model.Translation, error) {
	result := map[string]*model.Translation{}
	if d != nil {
		rule := dev.ToTranslationRule(dev, d)
		result[d.Name] = &model.Translation{
			Interactive: true,
			Name:        dev.Name,
			Marker:      dev.DevPath,
			Version:     model.TranslationVersion,
			Deployment:  d,
			Replicas:    *d.Spec.Replicas,
			Rules:       []*model.TranslationRule{rule},
		}
	}
	for _, s := range dev.Services {
		d, err := Get(s, dev.Namespace, c)
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		rule := s.ToTranslationRule(dev, d)
		if _, ok := result[d.Name]; ok {
			result[d.Name].Rules = append(result[d.Name].Rules, rule)
		} else {
			result[d.Name] = &model.Translation{
				Name:        dev.Name,
				Interactive: false,
				Version:     model.TranslationVersion,
				Deployment:  d,
				Replicas:    *d.Spec.Replicas,
				Rules:       []*model.TranslationRule{rule},
			}
		}
	}
	return result, nil
}

//Deploy creates or updates a deployment
func Deploy(d *appsv1.Deployment, forceCreate bool, client *kubernetes.Clientset) error {
	if forceCreate {
		if err := create(d, client); err != nil {
			return err
		}
	} else {
		if err := update(d, client); err != nil {
			return err
		}
	}
	return nil
}

//TraslateDevMode translates the deployment manifests to put them in dev mode
func TraslateDevMode(tr map[string]*model.Translation, ns string, c *kubernetes.Clientset) error {
	for _, t := range tr {
		err := translate(t, ns, c)
		if err != nil {
			return err
		}
	}
	return nil
}

//IsDevModeOn returns if a deployment is in devmode
func IsDevModeOn(d *appsv1.Deployment) bool {
	labels := d.GetObjectMeta().GetLabels()
	if labels == nil {
		return false
	}
	_, ok := labels[OktetoDevLabel]
	return ok
}

// DevModeOff deactivates dev mode for d
func DevModeOff(d *appsv1.Deployment, c *kubernetes.Clientset) error {
	trRulesJSON := getAnnotation(d.Spec.Template.GetObjectMeta(), OktetoTranslationAnnotation)
	if len(trRulesJSON) == 0 {
		dManifest := getAnnotation(d.GetObjectMeta(), oktetoDeploymentAnnotation)
		if len(dManifest) == 0 {
			log.Infof("%s/%s is not an okteto environment", d.Namespace, d.Name)
			return nil
		}
		dOrig := &appsv1.Deployment{}
		if err := json.Unmarshal([]byte(dManifest), dOrig); err != nil {
			return fmt.Errorf("malformed manifest: %s", err)
		}
		d = dOrig
	} else {
		trRules := &model.Translation{}
		if err := json.Unmarshal([]byte(trRulesJSON), trRules); err != nil {
			return fmt.Errorf("malformed tr rules: %s", err)
		}
		d.Spec.Replicas = &trRules.Replicas
		annotations := d.GetObjectMeta().GetAnnotations()
		delete(annotations, oktetoDeveloperAnnotation)
		delete(annotations, oktetoVersionAnnotation)
		d.GetObjectMeta().SetAnnotations(annotations)
		annotations = d.Spec.Template.GetObjectMeta().GetAnnotations()
		delete(annotations, OktetoTranslationAnnotation)
		d.Spec.Template.GetObjectMeta().SetAnnotations(annotations)
		labels := d.GetObjectMeta().GetLabels()
		delete(labels, OktetoDevLabel)
		d.GetObjectMeta().SetLabels(labels)
	}

	if err := update(d, c); err != nil {
		return err
	}

	return nil
}

func create(d *appsv1.Deployment, c *kubernetes.Clientset) error {
	log.Debugf("creating deployment %s/%s", d.Namespace, d.Name)
	_, err := c.AppsV1().Deployments(d.Namespace).Create(d)
	if err != nil {
		return err
	}
	return nil
}

func update(d *appsv1.Deployment, c *kubernetes.Clientset) error {
	log.Debugf("updating deployment %s/%s", d.Namespace, d.Name)
	d.ResourceVersion = ""
	d.Status = appsv1.DeploymentStatus{}
	_, err := c.AppsV1().Deployments(d.Namespace).Update(d)
	if err != nil {
		return err
	}
	return nil
}

//Destroy destroys a k8s service
func Destroy(dev *model.Dev, c *kubernetes.Clientset) error {
	log.Infof("deleting deployment '%s'...", dev.Name)
	dClient := c.AppsV1().Deployments(dev.Namespace)
	err := dClient.Delete(dev.Name, &metav1.DeleteOptions{GracePeriodSeconds: &devTerminationGracePeriodSeconds})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			log.Infof("deployment '%s' was already deleted.", dev.Name)
			return nil
		}
		return fmt.Errorf("error deleting kubernetes deployment: %s", err)
	}
	log.Infof("deployment '%s' deleted", dev.Name)
	return nil
}
