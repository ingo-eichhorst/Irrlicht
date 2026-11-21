// A stand-in for Herdr's `herdr` module; see stubs/gpui-kit.js. A test sets
// `world` (the agents, terminals and theme Herdr would report) and reads back
// `world.ran`, every command main.js ran.

export const world = { agents: [], terminals: [], theme: {}, ran: [] };

export const agents = () => world.agents;
export const terminals = () => world.terminals;
export const theme = () => world.theme;
export const run = (command, args) => {
  world.ran.push([command, args]);
  return null;
};
// checking() is true, as under `herdr-app check`: init then starts no poll,
// runs no program and waits for no command, and the test sets the state.
export const checking = () => true;
export const exec = async () => ({ code: 1, stdout: '', stderr: '' });
export const home = () => '/home/dev';
export const readTextFile = async () => null;
export const nextCommand = () => new Promise(() => {});
