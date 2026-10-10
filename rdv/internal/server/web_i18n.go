package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Languages of the account pages and emails. French uses the formal "vous".
var webText = map[string]map[string]string{
	"en": {
		"brand":            "Oblireach",
		"landing.title":    "Oblireach Support",
		"landing.p1":       "This server connects Oblireach Desktop operators with the people they help. It never sees the screens, keystrokes or files exchanged: sessions are encrypted end to end.",
		"landing.p2":       "People who receive help never need an account. Operators sign in from Oblireach Desktop to connect to another computer by its ID and code.",
		"landing.register": "Create an operator account",
		"landing.noacc":    "Operator accounts are not available on this server: ask its administrator for an operator key.",
		"nav.help":         "Sign-in help",
		"nav.terms":        "Terms of use",
		"nav.home":         "Home",

		"reg.title":         "Create an operator account",
		"reg.intro":         "An operator account lets you connect to computers whose owner gives you their ID and one-time code. The person always has to accept the connection.",
		"reg.name":          "Display name",
		"reg.name.hint":     "Shown to the people you help, next to your email address.",
		"reg.email":         "Email address",
		"reg.password":      "Password",
		"reg.password.hint": "At least 10 characters. Avoid common passwords.",
		"reg.confirm":       "Confirm the password",
		"reg.terms":         "I accept the terms of use",
		"reg.terms.link":    "terms of use",
		"reg.terms.pre":     "I accept the",
		"reg.submit":        "Create the account",
		"reg.hp":            "Leave this field empty",
		"reg.have":          "Already have an account? Sign in from Oblireach Desktop.",

		"err.name":        "Enter a display name of 1 to 40 characters, without < > @ brackets or quotes.",
		"err.email":       "Enter a valid email address (64 characters at most).",
		"err.email.taken": "This email address cannot be used.",
		"err.pw.short":    "The password must have at least 10 characters.",
		"err.pw.long":     "The password is too long (128 characters at most).",
		"err.pw.common":   "This password is too common or too easy to guess. Choose another one.",
		"err.pw.match":    "The two passwords do not match.",
		"err.terms":       "You must accept the terms of use.",
		"err.form":        "This form has expired. Please submit it again.",
		"err.server":      "The server could not process the request. Please try again later.",

		"sent.title":  "Check your inbox",
		"sent.p1":     "If this address can be used, you will receive an email with a confirmation link within a few minutes. The link is valid for 24 hours.",
		"sent.p2":     "Nothing arrived? Check your spam folder, or register again in a few minutes.",
		"ready.title": "Your account is ready",
		"ready.p1":    "Sign in from Oblireach Desktop with your email address and password.",
		"ready.p2":    "This server does not verify email addresses: the people you help will see your address marked as unverified.",

		"verify.title":     "Confirm your email address",
		"verify.p1":        "Confirm to activate your operator account.",
		"verify.submit":    "Confirm my address",
		"verify.bad.title": "Invalid or expired link",
		"verify.bad.p1":    "This link is invalid, was already used, or has expired. Register again to receive a new one.",
		"verify.ok.title":  "Address confirmed",
		"verify.ok.p1":     "Your operator account is active. Sign in from Oblireach Desktop with your email address and password.",

		"help.title": "Signing in",
		"help.p1":    "Open Oblireach Desktop, then select Login on the Dashboard and enter the email address and password of your operator account.",
		"help.p2":    "Your email address must be confirmed first: open the link received after registering. You can register again to receive a new link.",
		"help.p3":    "Forgotten password: password reset is not available yet. Contact the administrator of this server.",

		"terms.title": "Terms of use",
		"terms.p1":    "The administrator of this server will publish its full terms of use here.",
		"terms.p2":    "In short: connect only to computers whose owner asked you for help and gave you their code themselves. Unsolicited access, scams and fake technical support are forbidden and lead to the suspension of the account.",
		"terms.p3":    "The server keeps your email address, display name, password hash, account dates and usage counters, and nothing else. Accounts are deleted after 24 months without sign-in, or on request to the administrator.",

		"error.title":    "Something went wrong",
		"limit.title":    "Too many attempts",
		"limit.p1":       "Please try again in a few minutes.",
		"notfound.title": "Page not found",
		"notfound.p1":    "This page does not exist.",
		"disabled.p1":    "Operator accounts are not available on this server.",

		"mail.verify.subject": "Confirm your Oblireach operator account",
		"mail.verify.body":    "Hello,\n\nTo activate your Oblireach operator account on %s, open this link within 24 hours, then confirm:\n\n%s\n\nIf you did not create this account, ignore this message: nothing will be activated.\n",
		"mail.exists.subject": "Your Oblireach operator account",
		"mail.exists.body":    "Hello,\n\nSomeone (perhaps you) tried to create an Oblireach operator account on %s with this email address, which already has one. You can sign in from Oblireach Desktop.\n\nIf it was not you, you can ignore this message.\n",
	},
	"fr": {
		"brand":            "Oblireach",
		"landing.title":    "Oblireach Support",
		"landing.p1":       "Ce serveur met en relation les opérateurs d'Oblireach Desktop et les personnes qu'ils aident. Il ne voit jamais les écrans, les frappes ni les fichiers échangés : les sessions sont chiffrées de bout en bout.",
		"landing.p2":       "Les personnes aidées n'ont jamais besoin de compte. Les opérateurs se connectent depuis Oblireach Desktop pour joindre un autre ordinateur par son ID et son code.",
		"landing.register": "Créer un compte opérateur",
		"landing.noacc":    "Les comptes opérateurs ne sont pas disponibles sur ce serveur : demandez une clé d'opérateur à son administrateur.",
		"nav.help":         "Aide à la connexion",
		"nav.terms":        "Conditions d'utilisation",
		"nav.home":         "Accueil",

		"reg.title":         "Créer un compte opérateur",
		"reg.intro":         "Un compte opérateur vous permet de vous connecter aux ordinateurs dont le propriétaire vous communique l'ID et le code à usage unique. La personne doit toujours accepter la connexion.",
		"reg.name":          "Nom affiché",
		"reg.name.hint":     "Montré aux personnes que vous aidez, à côté de votre adresse e-mail.",
		"reg.email":         "Adresse e-mail",
		"reg.password":      "Mot de passe",
		"reg.password.hint": "10 caractères au moins. Évitez les mots de passe courants.",
		"reg.confirm":       "Confirmez le mot de passe",
		"reg.terms":         "J'accepte les conditions d'utilisation",
		"reg.terms.link":    "conditions d'utilisation",
		"reg.terms.pre":     "J'accepte les",
		"reg.submit":        "Créer le compte",
		"reg.hp":            "Laissez ce champ vide",
		"reg.have":          "Vous avez déjà un compte ? Connectez-vous depuis Oblireach Desktop.",

		"err.name":        "Saisissez un nom affiché de 1 à 40 caractères, sans < > @, crochets ni guillemets.",
		"err.email":       "Saisissez une adresse e-mail valide (64 caractères au plus).",
		"err.email.taken": "Cette adresse e-mail ne peut pas être utilisée.",
		"err.pw.short":    "Le mot de passe doit comporter au moins 10 caractères.",
		"err.pw.long":     "Le mot de passe est trop long (128 caractères au plus).",
		"err.pw.common":   "Ce mot de passe est trop courant ou trop facile à deviner. Choisissez-en un autre.",
		"err.pw.match":    "Les deux mots de passe ne correspondent pas.",
		"err.terms":       "Vous devez accepter les conditions d'utilisation.",
		"err.form":        "Ce formulaire a expiré. Veuillez le renvoyer.",
		"err.server":      "Le serveur n'a pas pu traiter la demande. Veuillez réessayer plus tard.",

		"sent.title":  "Consultez votre boîte de réception",
		"sent.p1":     "Si cette adresse peut être utilisée, vous recevrez dans quelques minutes un e-mail contenant un lien de confirmation. Le lien est valable 24 heures.",
		"sent.p2":     "Rien reçu ? Vérifiez vos courriers indésirables, ou inscrivez-vous de nouveau dans quelques minutes.",
		"ready.title": "Votre compte est prêt",
		"ready.p1":    "Connectez-vous depuis Oblireach Desktop avec votre adresse e-mail et votre mot de passe.",
		"ready.p2":    "Ce serveur ne vérifie pas les adresses e-mail : les personnes que vous aidez verront votre adresse signalée comme non vérifiée.",

		"verify.title":     "Confirmez votre adresse e-mail",
		"verify.p1":        "Confirmez pour activer votre compte opérateur.",
		"verify.submit":    "Confirmer mon adresse",
		"verify.bad.title": "Lien invalide ou expiré",
		"verify.bad.p1":    "Ce lien est invalide, a déjà été utilisé ou a expiré. Inscrivez-vous de nouveau pour en recevoir un autre.",
		"verify.ok.title":  "Adresse confirmée",
		"verify.ok.p1":     "Votre compte opérateur est actif. Connectez-vous depuis Oblireach Desktop avec votre adresse e-mail et votre mot de passe.",

		"help.title": "Se connecter",
		"help.p1":    "Ouvrez Oblireach Desktop, choisissez Connexion sur le tableau de bord, puis saisissez l'adresse e-mail et le mot de passe de votre compte opérateur.",
		"help.p2":    "Votre adresse e-mail doit d'abord être confirmée : ouvrez le lien reçu après l'inscription. Vous pouvez vous inscrire de nouveau pour recevoir un nouveau lien.",
		"help.p3":    "Mot de passe oublié : la réinitialisation n'est pas encore disponible. Contactez l'administrateur de ce serveur.",

		"terms.title": "Conditions d'utilisation",
		"terms.p1":    "L'administrateur de ce serveur publiera ici ses conditions d'utilisation complètes.",
		"terms.p2":    "En bref : connectez-vous uniquement aux ordinateurs dont le propriétaire vous a demandé de l'aide et vous a communiqué lui-même son code. Les accès non sollicités, les arnaques et le faux support technique sont interdits et entraînent la suspension du compte.",
		"terms.p3":    "Le serveur conserve votre adresse e-mail, votre nom affiché, l'empreinte de votre mot de passe, les dates de votre compte et vos compteurs d'utilisation, et rien d'autre. Les comptes sont supprimés après 24 mois sans connexion, ou sur demande auprès de l'administrateur.",

		"error.title":    "Une erreur est survenue",
		"limit.title":    "Trop de tentatives",
		"limit.p1":       "Veuillez réessayer dans quelques minutes.",
		"notfound.title": "Page introuvable",
		"notfound.p1":    "Cette page n'existe pas.",
		"disabled.p1":    "Les comptes opérateurs ne sont pas disponibles sur ce serveur.",

		"mail.verify.subject": "Confirmez votre compte opérateur Oblireach",
		"mail.verify.body":    "Bonjour,\n\nPour activer votre compte opérateur Oblireach sur %s, ouvrez ce lien dans les 24 heures, puis confirmez :\n\n%s\n\nSi vous n'avez pas créé ce compte, ignorez ce message : rien ne sera activé.\n",
		"mail.exists.subject": "Votre compte opérateur Oblireach",
		"mail.exists.body":    "Bonjour,\n\nQuelqu'un (peut-être vous) a tenté de créer un compte opérateur Oblireach sur %s avec cette adresse e-mail, qui en possède déjà un. Vous pouvez vous connecter depuis Oblireach Desktop.\n\nSi ce n'était pas vous, vous pouvez ignorer ce message.\n",
	},
}

// pickLang chooses en or fr from Accept-Language (q-values honoured).
func pickLang(r *http.Request) string {
	type cand struct {
		tag string
		q   float64
	}
	var cs []cand
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		primary, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if q > 0 && (primary == "en" || primary == "fr") {
			cs = append(cs, cand{primary, q})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].q > cs[j].q })
	if len(cs) > 0 {
		return cs[0].tag
	}
	return "en"
}
