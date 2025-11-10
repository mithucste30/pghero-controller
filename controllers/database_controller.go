package controllers

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pgherov1alpha1 "github.com/mithucste30/pghero-controller/api/v1alpha1"
)

const (
	databaseFinalizer = "pghero.mithucste30.io/finalizer"
	configMapName     = "pghero-databases"
)

// DatabaseReconciler reconciles a Database object
type DatabaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=pghero.mithucste30.io,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pghero.mithucste30.io,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pghero.mithucste30.io,resources=databases/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile handles the reconciliation logic for Database resources
func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Database instance
	database := &pgherov1alpha1.Database{}
	err := r.Get(ctx, req.NamespacedName, database)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Database resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Database")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !database.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, database)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(database, databaseFinalizer) {
		controllerutil.AddFinalizer(database, databaseFinalizer)
		if err := r.Update(ctx, database); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Get database URL
	dbURL, err := r.getDatabaseURL(ctx, database)
	if err != nil {
		return r.updateStatus(ctx, database, "Error", fmt.Sprintf("Failed to get database URL: %v", err), "")
	}

	// Create or update ConfigMap
	configMapRef, err := r.reconcileConfigMap(ctx, database, dbURL)
	if err != nil {
		return r.updateStatus(ctx, database, "Error", fmt.Sprintf("Failed to reconcile ConfigMap: %v", err), "")
	}

	// Update status
	return r.updateStatus(ctx, database, "Ready", "Database configuration synchronized", configMapRef)
}

// getDatabaseURL retrieves the database URL from either the spec or a secret
func (r *DatabaseReconciler) getDatabaseURL(ctx context.Context, database *pgherov1alpha1.Database) (string, error) {
	// If urlFromSecret is specified, get URL from secret
	if database.Spec.URLFromSecret != nil {
		secretRef := database.Spec.URLFromSecret
		namespace := secretRef.Namespace
		if namespace == "" {
			namespace = database.Namespace
		}

		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      secretRef.Name,
			Namespace: namespace,
		}, secret)
		if err != nil {
			return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretRef.Name, err)
		}

		url, ok := secret.Data[secretRef.Key]
		if !ok {
			return "", fmt.Errorf("key %s not found in secret %s/%s", secretRef.Key, namespace, secretRef.Name)
		}

		return string(url), nil
	}

	// Otherwise, use the URL from spec
	return database.Spec.URL, nil
}

// reconcileConfigMap creates or updates the aggregated ConfigMap with all database configurations
func (r *DatabaseReconciler) reconcileConfigMap(ctx context.Context, database *pgherov1alpha1.Database, dbURL string) (string, error) {
	logger := log.FromContext(ctx)

	// Use a single aggregated ConfigMap name
	configMapName := "pghero-databases"

	// List all Database resources in the namespace
	databaseList := &pgherov1alpha1.DatabaseList{}
	if err := r.List(ctx, databaseList, client.InNamespace(database.Namespace)); err != nil {
		return "", fmt.Errorf("failed to list databases: %w", err)
	}

	// Build aggregated configuration
	aggregatedConfig := "databases:\n"
	for _, db := range databaseList.Items {
		var url string
		var err error

		// Get database URL for each database
		if db.Name == database.Name {
			url = dbURL // Use the already fetched URL for current database
		} else {
			url, err = r.getDatabaseURL(ctx, &db)
			if err != nil {
				logger.Error(err, "Failed to get database URL", "Database", db.Name)
				continue
			}
		}

		if db.Spec.Enabled {
			aggregatedConfig += fmt.Sprintf("  %s:\n", db.Spec.Name)
			aggregatedConfig += fmt.Sprintf("    url: %s\n", url)
		}
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: database.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "pghero",
				"app.kubernetes.io/component":  "database-config",
				"app.kubernetes.io/managed-by": "pghero-controller",
			},
			Annotations: map[string]string{
				"pghero.mithucste30.io/database-count": fmt.Sprintf("%d", len(databaseList.Items)),
			},
		},
		Data: map[string]string{
			"database.yml": aggregatedConfig,
		},
	}

	// Create or update ConfigMap
	found := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: database.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		logger.Info("Creating aggregated ConfigMap", "ConfigMap.Namespace", configMap.Namespace, "ConfigMap.Name", configMap.Name)
		err = r.Create(ctx, configMap)
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		// Update existing ConfigMap
		found.Data = configMap.Data
		found.Labels = configMap.Labels
		found.Annotations = configMap.Annotations
		logger.Info("Updating aggregated ConfigMap", "ConfigMap.Namespace", found.Namespace, "ConfigMap.Name", found.Name, "DatabaseCount", len(databaseList.Items))
		err = r.Update(ctx, found)
		if err != nil {
			return "", err
		}
	}

	return configMapName, nil
}

// generateDatabaseConfig generates the YAML configuration for PgHero
func (r *DatabaseReconciler) generateDatabaseConfig(database *pgherov1alpha1.Database, dbURL string) string {
	enabled := "true"
	if !database.Spec.Enabled {
		enabled = "false"
	}

	config := fmt.Sprintf(`databases:
  %s:
    url: %s
    enabled: %s
`, database.Spec.Name, dbURL, enabled)

	return config
}

// updateStatus updates the status of the Database resource
func (r *DatabaseReconciler) updateStatus(ctx context.Context, database *pgherov1alpha1.Database, phase, message, configMapRef string) (ctrl.Result, error) {
	database.Status.Phase = phase
	database.Status.Message = message
	database.Status.LastUpdated = metav1.Now()
	database.Status.ConfigMapRef = configMapRef

	// Update conditions
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	if phase == "Error" {
		condition.Status = metav1.ConditionFalse
	}

	// Update or append condition
	found := false
	for i, c := range database.Status.Conditions {
		if c.Type == "Ready" {
			database.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		database.Status.Conditions = append(database.Status.Conditions, condition)
	}

	if err := r.Status().Update(ctx, database); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue after 5 minutes to ensure config is in sync
	if phase == "Ready" {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of a Database resource
func (r *DatabaseReconciler) handleDeletion(ctx context.Context, database *pgherov1alpha1.Database) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(database, databaseFinalizer) {
		// Update the aggregated ConfigMap to remove this database
		logger.Info("Updating aggregated ConfigMap after deletion", "Database.Name", database.Name)

		// Rebuild aggregated ConfigMap without this database
		if err := r.rebuildAggregatedConfigMap(ctx, database.Namespace, database.Name); err != nil {
			logger.Error(err, "Failed to rebuild aggregated ConfigMap")
			// Continue with deletion even if ConfigMap update fails
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(database, databaseFinalizer)
		if err := r.Update(ctx, database); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// rebuildAggregatedConfigMap rebuilds the aggregated ConfigMap excluding a specific database
func (r *DatabaseReconciler) rebuildAggregatedConfigMap(ctx context.Context, namespace, excludeDB string) error {
	logger := log.FromContext(ctx)
	configMapName := "pghero-databases"

	// List all Database resources in the namespace
	databaseList := &pgherov1alpha1.DatabaseList{}
	if err := r.List(ctx, databaseList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list databases: %w", err)
	}

	// Build aggregated configuration, excluding the deleted database
	aggregatedConfig := "databases:\n"
	count := 0
	for _, db := range databaseList.Items {
		if db.Name == excludeDB {
			continue // Skip the database being deleted
		}

		url, err := r.getDatabaseURL(ctx, &db)
		if err != nil {
			logger.Error(err, "Failed to get database URL", "Database", db.Name)
			continue
		}

		if db.Spec.Enabled {
			aggregatedConfig += fmt.Sprintf("  %s:\n", db.Spec.Name)
			aggregatedConfig += fmt.Sprintf("    url: %s\n", url)
			count++
		}
	}

	// Get existing ConfigMap
	configMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, configMap)
	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist, nothing to update
			return nil
		}
		return err
	}

	// Update ConfigMap
	configMap.Data["database.yml"] = aggregatedConfig
	configMap.Annotations["pghero.mithucste30.io/database-count"] = fmt.Sprintf("%d", count)

	logger.Info("Updating aggregated ConfigMap", "ConfigMap.Name", configMapName, "DatabaseCount", count)
	return r.Update(ctx, configMap)
}

// SetupWithManager sets up the controller with the Manager
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pgherov1alpha1.Database{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
