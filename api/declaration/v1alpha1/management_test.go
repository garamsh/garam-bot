package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
)

const clusterGRN = "grn:acme/iam/cluster/c-1"

func object(labels, annotations map[string]string) *v1alpha1.Bot {
	return &v1alpha1.Bot{ObjectMeta: metav1.ObjectMeta{Name: "mira", Labels: labels, Annotations: annotations}}
}

func garamMarker() (map[string]string, map[string]string) {
	return map[string]string{v1alpha1.LabelManagedBy: "garam"}, map[string]string{
		v1alpha1.AnnotationCluster:               clusterGRN,
		v1alpha1.AnnotationDeclaration:           "decl-42",
		v1alpha1.AnnotationDeclarationGeneration: "3",
	}
}

func TestManagementOf_OWN1_AnObjectWithoutTheLabelIsTheClusters(t *testing.T) {
	for name, labels := range map[string]map[string]string{
		"no label":      nil,
		"label cluster": {v1alpha1.LabelManagedBy: "cluster"},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := v1alpha1.ManagementOf(object(labels, nil))
			require.NoError(t, err)
			assert.Equal(t, v1alpha1.OwnerCluster, m.Owner())
			_, garam := m.Garam()
			assert.False(t, garam)
		})
	}
}

func TestManagementOf_OWN1_AnotherLabelValueIsRefused(t *testing.T) {
	_, err := v1alpha1.ManagementOf(object(map[string]string{v1alpha1.LabelManagedBy: "argocd"}, nil))
	require.ErrorIs(t, err, v1alpha1.ErrInvalidMarker)
}

func TestManagementOf_OWN2_AGaramOwnedObjectNamesItsDeclaration(t *testing.T) {
	labels, annotations := garamMarker()
	m, err := v1alpha1.ManagementOf(object(labels, annotations))
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.OwnerGaram, m.Owner())
	d, garam := m.Garam()
	require.True(t, garam)
	assert.Equal(t, v1alpha1.GaramDeclaration{Cluster: clusterGRN, ID: "decl-42", Generation: 3}, d)
}

func TestManagementOf_OWN2_AGaramOwnedObjectWithAMalformedDeclarationIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		drop             bool
	}{
		{name: "no cluster", key: v1alpha1.AnnotationCluster, drop: true},
		{name: "no declaration", key: v1alpha1.AnnotationDeclaration, drop: true},
		{name: "no generation", key: v1alpha1.AnnotationDeclarationGeneration, drop: true},
		{name: "a GRN of another resource", key: v1alpha1.AnnotationCluster, value: "grn:acme/platform/bot/mira"},
		{name: "a GRN without an organization", key: v1alpha1.AnnotationCluster, value: "grn:/iam/cluster/c-1"},
		{name: "not a GRN", key: v1alpha1.AnnotationCluster, value: "c-1"},
		{name: "an empty declaration", key: v1alpha1.AnnotationDeclaration, value: ""},
		{name: "a declaration with a space", key: v1alpha1.AnnotationDeclaration, value: "decl 42"},
		{name: "generation zero", key: v1alpha1.AnnotationDeclarationGeneration, value: "0"},
		{name: "a negative generation", key: v1alpha1.AnnotationDeclarationGeneration, value: "-3"},
		{name: "a generation with a leading zero", key: v1alpha1.AnnotationDeclarationGeneration, value: "03"},
		{name: "a generation that is not a number", key: v1alpha1.AnnotationDeclarationGeneration, value: "three"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels, annotations := garamMarker()
			// The control: the marker is accepted before the one change the case makes.
			_, err := v1alpha1.ManagementOf(object(labels, annotations))
			require.NoError(t, err)

			if tc.drop {
				delete(annotations, tc.key)
			} else {
				annotations[tc.key] = tc.value
			}
			_, err = v1alpha1.ManagementOf(object(labels, annotations))
			require.ErrorIs(t, err, v1alpha1.ErrInvalidMarker)
		})
	}
}

func TestManagementOf_OWN3_AClusterOwnedObjectCarryingADeclarationIsRefused(t *testing.T) {
	_, annotations := garamMarker()
	for key, value := range annotations {
		t.Run(key, func(t *testing.T) {
			_, err := v1alpha1.ManagementOf(object(nil, map[string]string{key: value}))
			require.ErrorIs(t, err, v1alpha1.ErrInvalidMarker)
		})
	}
}

func TestMarkGaramOwned_WritesTheMarkerManagementOfReads(t *testing.T) {
	bot := object(map[string]string{v1alpha1.LabelManagedBy: "cluster", "team": "game"}, nil)
	d := v1alpha1.GaramDeclaration{Cluster: clusterGRN, ID: "decl-42", Generation: 3}
	require.NoError(t, v1alpha1.MarkGaramOwned(bot, d))

	m, err := v1alpha1.ManagementOf(bot)
	require.NoError(t, err)
	got, garam := m.Garam()
	require.True(t, garam)
	assert.Equal(t, d, got)
	assert.Equal(t, "game", bot.Labels["team"])
}

func TestMarkGaramOwned_RefusesAMalformedDeclaration(t *testing.T) {
	bot := object(nil, nil)
	err := v1alpha1.MarkGaramOwned(bot, v1alpha1.GaramDeclaration{Cluster: clusterGRN, ID: "decl-42"})
	require.ErrorIs(t, err, v1alpha1.ErrInvalidMarker)
	assert.Empty(t, bot.Labels)
}
