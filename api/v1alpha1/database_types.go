package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatabaseSpec defines the desired state of Database
type DatabaseSpec struct {
	// Name is a friendly name for the database connection
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// URL is the database connection URL
	// Can reference a secret using syntax: secret://namespace/secret-name/key
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// URLFromSecret references a Kubernetes secret containing the database URL
	// +optional
	URLFromSecret *SecretReference `json:"urlFromSecret,omitempty"`

	// SuperuserURL is an optional connection URL with superuser privileges for automatic extension setup
	// +optional
	SuperuserURL string `json:"superuserUrl,omitempty"`

	// SuperuserURLFromSecret references a Kubernetes secret containing superuser credentials
	// +optional
	SuperuserURLFromSecret *SecretReference `json:"superuserUrlFromSecret,omitempty"`

	// DatabaseType specifies the type of database (postgresql, mysql, etc.)
	// +kubebuilder:validation:Enum=postgresql;mysql
	// +kubebuilder:default=postgresql
	DatabaseType string `json:"databaseType,omitempty"`

	// Enabled determines if this database connection should be active in PgHero
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`
}

// SecretReference contains information to locate a secret
type SecretReference struct {
	// Name is the name of the secret
	Name string `json:"name"`

	// Key is the key within the secret
	Key string `json:"key"`

	// Namespace is the namespace of the secret (defaults to same namespace as Database resource)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// DatabaseStatus defines the observed state of Database
type DatabaseStatus struct {
	// Phase represents the current phase of the database connection
	// +kubebuilder:validation:Enum=Pending;Configuring;Ready;Error
	Phase string `json:"phase,omitempty"`

	// Message provides additional information about the current status
	// +optional
	Message string `json:"message,omitempty"`

	// LastUpdated is the timestamp when the status was last updated
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// ConfigMapRef references the ConfigMap where the database configuration is stored
	// +optional
	ConfigMapRef string `json:"configMapRef,omitempty"`

	// ConnectionStatus indicates if the database is reachable
	// +optional
	ConnectionStatus string `json:"connectionStatus,omitempty"`

	// ExtensionsReady indicates if required extensions are installed and configured
	// +optional
	ExtensionsReady bool `json:"extensionsReady,omitempty"`

	// RequiredExtensions lists the extensions that need to be installed
	// +optional
	RequiredExtensions []string `json:"requiredExtensions,omitempty"`

	// InstalledExtensions lists the extensions that are currently installed
	// +optional
	InstalledExtensions []string `json:"installedExtensions,omitempty"`

	// LastError stores the last error encountered during setup
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Conditions represent the latest available observations of the Database's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=db;pgdb
// +kubebuilder:printcolumn:name="Database Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.databaseType`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Database is the Schema for the databases API
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseSpec   `json:"spec,omitempty"`
	Status DatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseList contains a list of Database
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Database{}, &DatabaseList{})
}
