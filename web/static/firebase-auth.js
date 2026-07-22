(function () {
    'use strict';

    const localDevelopment = ['localhost', '127.0.0.1', '::1'].includes(window.location.hostname);
    const firebaseConfig = {
        apiKey: 'AIzaSyBKZUgrKoU8MxXIm9H0mGubjfL8TANdH5g',
        // Firebase's auth helper is reverse-proxied at /__/auth so auth state
        // stays first-party on production browsers that partition third-party
        // storage. Firebase custom auth domains require HTTPS, so HTTP
        // localhost uses the project's hosted helper and the popup flow.
        authDomain: localDevelopment ? 'open-swells-89714.firebaseapp.com' : window.location.host,
        projectId: 'open-swells-89714',
        storageBucket: 'open-swells-89714.appspot.com',
        messagingSenderId: '764680526760',
        appId: '1:764680526760:web:77bf8034f0f8df259b1b9d'
    };

    const app = firebase.apps.length ? firebase.app() : firebase.initializeApp(firebaseConfig);
    const auth = app.auth();
    const returnKey = 'open-swells-auth-return';
    let lastError = null;

    const persistenceReady = auth.setPersistence(firebase.auth.Auth.Persistence.LOCAL);

    function isMobileBrowser() {
        return /Android|iPhone|iPad|iPod|Mobile/i.test(navigator.userAgent);
    }

    function showError(error) {
        lastError = error;
        console.error('Firebase sign-in failed:', error);
        if (!error || error.code === 'auth/popup-closed-by-user') return;

        let notice = document.getElementById('firebaseAuthError');
        if (!notice) {
            notice = document.createElement('div');
            notice.id = 'firebaseAuthError';
            notice.setAttribute('role', 'alert');
            notice.style.cssText = 'position:fixed;left:50%;bottom:20px;z-index:10000;transform:translateX(-50%);max-width:calc(100vw - 32px);padding:10px 14px;background:#3a2024;border:1px solid #e06c75;color:#fff;font:12px/1.4 system-ui,sans-serif;box-shadow:0 8px 30px rgba(0,0,0,.35)';
            document.body.appendChild(notice);
        }
        notice.textContent = `Sign-in failed (${error.code || 'unknown error'}). Please try again.`;
    }

    const redirectReady = persistenceReady
        .then(() => auth.getRedirectResult())
        .then(result => {
            if (!result || !result.user) return result;
            const returnTo = sessionStorage.getItem(returnKey);
            sessionStorage.removeItem(returnKey);
            const current = window.location.pathname + window.location.search + window.location.hash;
            if (returnTo && returnTo.startsWith('/') && returnTo !== current) {
                window.location.replace(returnTo);
            }
            return result;
        })
        .catch(error => {
            sessionStorage.removeItem(returnKey);
            showError(error);
            return null;
        });

    async function signIn() {
        await redirectReady;
        const provider = new firebase.auth.GoogleAuthProvider();
        if (isMobileBrowser() && !localDevelopment) {
            sessionStorage.setItem(returnKey, window.location.pathname + window.location.search + window.location.hash);
            return auth.signInWithRedirect(provider);
        }
        return auth.signInWithPopup(provider);
    }

    async function authFetch(url, options) {
        await redirectReady;
        const user = auth.currentUser;
        if (!user) throw new Error('Not signed in');
        const token = await user.getIdToken();
        const requestOptions = options || {};
        requestOptions.headers = Object.assign({}, requestOptions.headers || {}, {
            Authorization: `Bearer ${token}`
        });
        return fetch(url, requestOptions);
    }

    window.openSwellsAuth = {
        auth,
        ready: redirectReady,
        signIn,
        signOut: () => auth.signOut(),
        authFetch,
        get lastError() { return lastError; }
    };
})();
