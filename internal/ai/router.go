package ai

// Router distribue les requêtes IA vers le bon provider selon la feature demandée.
type Router struct {
	local           AIProvider
	cloud           AIProvider
	routing         map[string]string // feature -> "local"|"cloud"|"off"
	defaultProvider string            // "local"|"cloud"|"off"
}

// NewRouter crée un routeur IA.
// local et cloud peuvent être nil si non configurés.
func NewRouter(local, cloud AIProvider, routing map[string]string, defaultProvider string) *Router {
	if routing == nil {
		routing = make(map[string]string)
	}
	return &Router{
		local:           local,
		cloud:           cloud,
		routing:         routing,
		defaultProvider: defaultProvider,
	}
}

// ForFeature retourne le provider approprié pour la feature donnée.
// Retourne nil si la feature est désactivée ou si aucun provider n'est disponible.
func (r *Router) ForFeature(feature string) AIProvider {
	pref := r.routing[feature]
	if pref == "" {
		pref = r.defaultProvider
	}

	if pref == "off" {
		return nil
	}

	primary, fallback := r.resolve(pref)
	if primary != nil && primary.Available() {
		return primary
	}
	if fallback != nil && fallback.Available() {
		return fallback
	}
	return nil
}

// resolve retourne le provider primaire et le fallback selon la préférence.
func (r *Router) resolve(pref string) (primary, fallback AIProvider) {
	switch pref {
	case "local":
		return r.local, r.cloud
	case "cloud":
		return r.cloud, r.local
	default:
		return nil, nil
	}
}

// Available retourne true si au moins un provider est disponible.
func (r *Router) Available() bool {
	if r.defaultProvider == "off" {
		return false
	}
	return (r.local != nil && r.local.Available()) ||
		(r.cloud != nil && r.cloud.Available())
}
