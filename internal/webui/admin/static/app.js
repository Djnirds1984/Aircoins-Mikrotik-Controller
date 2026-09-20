// Aircoins admin panel client script.
//
// The panel is server rendered; this file only provides the connection test,
// the save gate that depends on it, and confirmation for destructive actions.
// It is served as an external file because the panel sets a strict
// Content-Security-Policy that forbids inline script.
(function () {
  'use strict';

  function qs(selector, root) {
    return (root || document).querySelector(selector);
  }

  function qsa(selector, root) {
    return Array.prototype.slice.call((root || document).querySelectorAll(selector));
  }

  // ---------------------------------------------------------------- test -----

  var form = qs('#router-form');
  if (form) {
    setupConnectionTest(form);
  }

  function setupConnectionTest(form) {
    var testUrl = form.getAttribute('data-test-url');
    var isNew = form.getAttribute('data-is-new') === 'true';
    var resultBox = qs('#probe-result');
    var saveButton = qs('#save-router');
    var saveHint = qs('#save-hint');
    var testButton = qs('#test-connection');
    var csrfInput = qs('input[name="_csrf"]', form);

    // testedFingerprint records the endpoint the last successful test covered,
    // so editing the address afterwards re-locks saving.
    var testedFingerprint = null;

    function fingerprint() {
      var host = qs('input[name="host"]', form);
      var port = qs('input[name="api_port"]', form);
      var tls = qs('input[name="api_tls"]', form);
      var user = qs('input[name="api_user"]', form);
      return [
        (host ? host.value : '').trim().toLowerCase(),
        port ? port.value : '',
        tls && tls.checked ? '1' : '0',
        (user ? user.value : '').trim()
      ].join('|');
    }

    function setSaveEnabled(enabled, message) {
      if (saveButton) {
        saveButton.disabled = !enabled;
      }
      if (saveHint && message) {
        saveHint.textContent = message;
      }
    }

    function clearToken() {
      var token = form.querySelector('input[name="probe_token"]');
      if (token) {
        token.remove();
      }
    }

    function invalidate() {
      if (testedFingerprint === null) {
        return;
      }
      testedFingerprint = null;
      clearToken();
      setSaveEnabled(false, 'The address or credentials changed. Run the connection test again.');
    }

    qsa('[data-test-field]', form).forEach(function (field) {
      field.addEventListener('input', invalidate);
      field.addEventListener('change', invalidate);
    });

    if (!testButton) {
      return;
    }

    testButton.addEventListener('click', function () {
      var original = testButton.textContent;
      testButton.disabled = true;
      testButton.textContent = 'Testing…';
      if (resultBox) {
        resultBox.innerHTML = '<p class="muted">Contacting the router…</p>';
      }

      var headers = {};
      if (csrfInput) {
        headers['X-CSRF-Token'] = csrfInput.value;
      }

      fetch(testUrl, {
        method: 'POST',
        headers: headers,
        body: new FormData(form),
        credentials: 'same-origin'
      })
        .then(function (response) {
          return response.text();
        })
        .then(function (html) {
          if (resultBox) {
            resultBox.innerHTML = html;
          }
          var token = resultBox ? resultBox.querySelector('input[name="probe_token"]') : null;
          if (token && token.value) {
            testedFingerprint = fingerprint();
            setSaveEnabled(true, 'Connection verified. Save is unlocked.');
          } else {
            testedFingerprint = null;
            setSaveEnabled(false, 'The connection test did not pass.');
          }
        })
        .catch(function (error) {
          testedFingerprint = null;
          if (resultBox) {
            resultBox.innerHTML = '<div class="flash error">The test could not run: ' +
              String(error) + '</div>';
          }
          setSaveEnabled(false, 'The connection test could not run.');
        })
        .then(function () {
          testButton.disabled = false;
          testButton.textContent = original;
        });
    });

    // Last line of defence: the server also refuses an unverified save, but
    // stopping the submit here gives a clearer message.
    form.addEventListener('submit', function (event) {
      if (isNew && saveButton && saveButton.disabled) {
        event.preventDefault();
        return;
      }
      var skip = qs('input[name="skip_verification"]', form);
      if (skip && skip.checked) {
        return;
      }
      var token = form.querySelector('input[name="probe_token"]');
      if (!token || !token.value) {
        event.preventDefault();
        window.alert('Run the connection test successfully first, or tick "Save without verifying".');
      }
    });

    if (!isNew && saveButton) {
      saveButton.disabled = false;
      if (saveHint) {
        saveHint.textContent = '';
      }
    }
  }

  // ------------------------------------------------------------ confirm -----

  qsa('[data-confirm]').forEach(function (element) {
    element.addEventListener('click', function (event) {
      if (!window.confirm(element.getAttribute('data-confirm'))) {
        event.preventDefault();
      }
    });
  });
})();
