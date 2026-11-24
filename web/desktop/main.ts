import { mount } from 'svelte';
import '../src/app.css';
import Launcher from './Launcher.svelte';

mount(Launcher, { target: document.getElementById('app')! });
