package controllers

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/go-logr/logr"
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
		return r.updateStatus(ctx, database, "Error", fmt.Sprintf("Failed to get database URL: %v", err), "", false)
	}

	// Setup database extensions (only for PostgreSQL)
	if database.Spec.DatabaseType == "postgresql" || database.Spec.DatabaseType == "" {
		setupComplete, err := r.setupDatabaseExtensions(ctx, database, dbURL)
		if err != nil {
			logger.Error(err, "Failed to setup database extensions, will retry")
			return r.updateStatus(ctx, database, "Configuring", fmt.Sprintf("Setting up database extensions: %v", err), "", false)
		}
		if !setupComplete {
			logger.Info("Database extensions not ready yet, will retry")
			return r.updateStatus(ctx, database, "Configuring", "Setting up required database extensions", "", false)
		}
	}

	// Create or update ConfigMap
	configMapRef, err := r.reconcileConfigMap(ctx, database, dbURL)
	if err != nil {
		return r.updateStatus(ctx, database, "Error", fmt.Sprintf("Failed to reconcile ConfigMap: %v", err), "", database.Status.ExtensionsReady)
	}

	// Update status
	return r.updateStatus(ctx, database, "Ready", "Database configuration synchronized", configMapRef, true)
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

// getSuperuserURL retrieves the superuser database URL from either the spec or a secret
func (r *DatabaseReconciler) getSuperuserURL(ctx context.Context, database *pgherov1alpha1.Database, regularURL string) (string, error) {
	// If superuserUrlFromSecret is specified, get URL from secret
	if database.Spec.SuperuserURLFromSecret != nil {
		secretRef := database.Spec.SuperuserURLFromSecret
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
			return "", fmt.Errorf("failed to get superuser secret %s/%s: %w", namespace, secretRef.Name, err)
		}

		url, ok := secret.Data[secretRef.Key]
		if !ok {
			return "", fmt.Errorf("key %s not found in superuser secret %s/%s", secretRef.Key, namespace, secretRef.Name)
		}

		return string(url), nil
	}

	// If superuserUrl is specified, use it
	if database.Spec.SuperuserURL != "" {
		return database.Spec.SuperuserURL, nil
	}

	// No superuser credentials provided
	return "", nil
}

// createExtensionAsSuperuser creates an extension using superuser credentials
func (r *DatabaseReconciler) createExtensionAsSuperuser(ctx context.Context, superuserURL, extName string, database *pgherov1alpha1.Database, logger logr.Logger) bool {
	// Connect with superuser credentials
	superDB, err := sql.Open("postgres", superuserURL)
	if err != nil {
		logger.Error(err, "Failed to connect with superuser credentials")
		return false
	}
	defer superDB.Close()

	superDB.SetConnMaxLifetime(10 * time.Second)
	superDB.SetMaxOpenConns(1)

	if err := superDB.PingContext(ctx); err != nil {
		logger.Error(err, "Failed to ping database with superuser credentials")
		return false
	}

	// Create the extension
	createSQL := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s", extName)
	_, err = superDB.ExecContext(ctx, createSQL)
	if err != nil {
		logger.Error(err, "Failed to create extension as superuser", "Extension", extName)
		return false
	}

	// Extract username from regular URL to grant permissions
	// Parse the database URL to get the username
	username := extractUsernameFromURL(database.Spec.URL)
	if username != "" && username != "postgres" {
		// Grant pg_monitor role
		grantSQL := fmt.Sprintf("GRANT pg_monitor TO %s", username)
		_, err = superDB.ExecContext(ctx, grantSQL)
		if err != nil {
			logger.Error(err, "Failed to grant pg_monitor role", "User", username)
			// Continue anyway, extension is created
		}

		// Grant execute on reset function
		grantExecSQL := fmt.Sprintf("GRANT EXECUTE ON FUNCTION pg_stat_statements_reset TO %s", username)
		_, err = superDB.ExecContext(ctx, grantExecSQL)
		if err != nil {
			logger.Error(err, "Failed to grant execute permission", "User", username)
			// Continue anyway
		}

		logger.Info("Granted permissions to user", "User", username)
	}

	return true
}

// extractUsernameFromURL extracts the username from a PostgreSQL connection URL
func extractUsernameFromURL(dbURL string) string {
	// Format: postgres://username:password@host:port/database
	if !strings.HasPrefix(dbURL, "postgres://") && !strings.HasPrefix(dbURL, "postgresql://") {
		return ""
	}

	// Remove the protocol
	urlWithoutProtocol := strings.TrimPrefix(dbURL, "postgres://")
	urlWithoutProtocol = strings.TrimPrefix(urlWithoutProtocol, "postgresql://")

	// Extract username (before the colon)
	if idx := strings.Index(urlWithoutProtocol, ":"); idx > 0 {
		return urlWithoutProtocol[:idx]
	}

	// No password, check for @
	if idx := strings.Index(urlWithoutProtocol, "@"); idx > 0 {
		return urlWithoutProtocol[:idx]
	}

	return ""
}

// setupDatabaseExtensions checks and sets up required PostgreSQL extensions
func (r *DatabaseReconciler) setupDatabaseExtensions(ctx context.Context, database *pgherov1alpha1.Database, dbURL string) (bool, error) {
	logger := log.FromContext(ctx)

	// Required extensions for PgHero
	requiredExtensions := []string{"pg_stat_statements"}

	// Connect to the database
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		database.Status.ConnectionStatus = "Failed"
		database.Status.LastError = fmt.Sprintf("Failed to connect: %v", err)
		return false, fmt.Errorf("failed to open database connection: %w", err)
	}
	defer db.Close()

	// Set connection timeout
	db.SetConnMaxLifetime(10 * time.Second)
	db.SetMaxOpenConns(1)

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		database.Status.ConnectionStatus = "Unreachable"
		database.Status.LastError = fmt.Sprintf("Database unreachable: %v", err)
		return false, fmt.Errorf("database unreachable: %w", err)
	}

	database.Status.ConnectionStatus = "Connected"
	database.Status.RequiredExtensions = requiredExtensions

	// Check installed extensions
	rows, err := db.QueryContext(ctx, "SELECT extname FROM pg_extension")
	if err != nil {
		database.Status.LastError = fmt.Sprintf("Failed to query extensions: %v", err)
		return false, fmt.Errorf("failed to query extensions: %w", err)
	}
	defer rows.Close()

	installedExtensions := []string{}
	for rows.Next() {
		var extname string
		if err := rows.Scan(&extname); err != nil {
			continue
		}
		installedExtensions = append(installedExtensions, extname)
	}
	database.Status.InstalledExtensions = installedExtensions

	// Check if all required extensions are installed
	missingExtensions := []string{}
	for _, required := range requiredExtensions {
		found := false
		for _, installed := range installedExtensions {
			if installed == required {
				found = true
				break
			}
		}
		if !found {
			missingExtensions = append(missingExtensions, required)
		}
	}

	// If all extensions are installed, we're done
	if len(missingExtensions) == 0 {
		database.Status.ExtensionsReady = true
		database.Status.LastError = ""
		logger.Info("All required extensions are installed", "Database", database.Name)
		return true, nil
	}

	// Try to install missing extensions
	logger.Info("Attempting to install missing extensions", "Database", database.Name, "Missing", missingExtensions)

	for _, ext := range missingExtensions {
		createSQL := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s", ext)
		_, err := db.ExecContext(ctx, createSQL)
		if err != nil {
			// If we can't install, check if it's a permission error
			errMsg := err.Error()
			if strings.Contains(errMsg, "permission denied") || strings.Contains(errMsg, "must be superuser") {
				logger.Info("Permission denied with regular user, attempting with superuser credentials", "Extension", ext)

				// Try to get superuser URL
				superuserURL, err := r.getSuperuserURL(ctx, database, dbURL)
				if err != nil || superuserURL == "" {
					database.Status.LastError = fmt.Sprintf("Permission denied to create extension %s. Database user needs superuser privileges or provide superuser credentials via superuserUrl or superuserUrlFromSecret.", ext)
					database.Status.ExtensionsReady = false
					logger.Error(err, "No superuser credentials available", "Extension", ext)
					return false, nil
				}

				// Try with superuser credentials
				if !r.createExtensionAsSuperuser(ctx, superuserURL, ext, database, logger) {
					database.Status.LastError = fmt.Sprintf("Failed to create extension %s even with superuser credentials", ext)
					database.Status.ExtensionsReady = false
					return false, nil
				}
				logger.Info("Successfully installed extension with superuser credentials", "Extension", ext)
				continue
			}
			database.Status.LastError = fmt.Sprintf("Failed to create extension %s: %v", ext, err)
			return false, fmt.Errorf("failed to create extension %s: %w", ext, err)
		}
		logger.Info("Successfully installed extension", "Extension", ext)
	}

	// Verify extensions are now installed
	rows, err = db.QueryContext(ctx, "SELECT extname FROM pg_extension")
	if err != nil {
		return false, fmt.Errorf("failed to verify extensions: %w", err)
	}
	defer rows.Close()

	installedExtensions = []string{}
	for rows.Next() {
		var extname string
		if err := rows.Scan(&extname); err != nil {
			continue
		}
		installedExtensions = append(installedExtensions, extname)
	}
	database.Status.InstalledExtensions = installedExtensions

	// Check again
	allInstalled := true
	for _, required := range requiredExtensions {
		found := false
		for _, installed := range installedExtensions {
			if installed == required {
				found = true
				break
			}
		}
		if !found {
			allInstalled = false
			break
		}
	}

	database.Status.ExtensionsReady = allInstalled
	if allInstalled {
		database.Status.LastError = ""
		logger.Info("All extensions successfully installed", "Database", database.Name)
	}

	return allInstalled, nil
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
func (r *DatabaseReconciler) updateStatus(ctx context.Context, database *pgherov1alpha1.Database, phase, message, configMapRef string, extensionsReady bool) (ctrl.Result, error) {
	database.Status.Phase = phase
	database.Status.Message = message
	database.Status.LastUpdated = metav1.Now()
	database.Status.ConfigMapRef = configMapRef
	database.Status.ExtensionsReady = extensionsReady

	// Update conditions
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	if phase == "Error" || phase == "Configuring" {
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

	// Requeue based on phase
	if phase == "Ready" {
		// Requeue after 5 minutes to ensure config is in sync
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	} else if phase == "Configuring" {
		// Retry extension setup after 30 seconds
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	} else if phase == "Error" {
		// Retry errors after 1 minute
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
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
