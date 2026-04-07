import { ListCollections, GetCollectionColors } from '../../../wailsjs/go/main/App';
import type { main } from '../../../wailsjs/go/models';

class CollectionStore {
  collections = $state<main.CollectionInfo[]>([]);
  activeCollection = $state('');
  colors = $state<main.ColorEntry[]>([]);
  private loaded = false;

  async load() {
    this.collections = (await ListCollections()) ?? [];
    if (this.collections.length > 0 && !this.activeCollection) {
      this.activeCollection = this.collections[0].name;
    }
    if (this.activeCollection) {
      await this.loadColors(this.activeCollection);
    }
    this.loaded = true;
  }

  async loadColors(name: string) {
    this.colors = (await GetCollectionColors(name)) ?? [];
    this.syncCount(name, this.colors.length);
  }

  /** Update the count for a collection in the list. */
  private syncCount(name: string, count: number) {
    const idx = this.collections.findIndex(c => c.name === name);
    if (idx >= 0 && this.collections[idx].count !== count) {
      this.collections[idx] = { ...this.collections[idx], count };
    }
  }

  async select(name: string) {
    this.activeCollection = name;
    await this.loadColors(name);
  }

  /** Update the active collection's colors and sync the count in the list. */
  setColors(colors: main.ColorEntry[]) {
    this.colors = colors;
    if (this.activeCollection) {
      this.syncCount(this.activeCollection, colors.length);
    }
  }

  /** Reload the collection list (call after import/delete). */
  async refresh() {
    await this.load();
  }

  /** Ensure loaded at least once. */
  ensureLoaded() {
    if (!this.loaded) {
      this.load();
    }
  }
}

export const collectionStore = new CollectionStore();
