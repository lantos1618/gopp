// go++ VS Code extension: spawns `gopp lsp` (stdio) as the language server.
const vscode = require('vscode');
const { LanguageClient } = require('vscode-languageclient/node');

let client;

function activate(context) {
  const config = vscode.workspace.getConfiguration('gopp');
  const lspPath = config.get('lspPath', 'gopp');

  const serverOptions = {
    command: lspPath,
    args: ['lsp'],
  };

  const clientOptions = {
    documentSelector: [{ scheme: 'file', language: 'gopp' }],
  };

  client = new LanguageClient(
    'gopp',
    'go++ Language Server',
    serverOptions,
    clientOptions
  );

  client.start();
}

function deactivate() {
  if (!client) {
    return undefined;
  }
  return client.stop();
}

module.exports = { activate, deactivate };
