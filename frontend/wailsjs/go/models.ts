export namespace main {
	
	export class CollectionInfo {
	    name: string;
	    count: number;
	    builtIn: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CollectionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	        this.builtIn = source["builtIn"];
	    }
	}
	export class ColorEntry {
	    hex: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new ColorEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hex = source["hex"];
	        this.label = source["label"];
	    }
	}

}

export namespace pipeline {
	
	export class Options {
	    Input: string;
	    NumColors: number;
	    LockedColors: string[];
	    AutoColors: boolean;
	    Scale: number;
	    Output: string;
	    NozzleDiameter: number;
	    LayerHeight: number;
	    InventoryFile: string;
	    InventoryColors?: number[][];
	    InventoryLabels?: string[];
	    Brightness: number;
	    Contrast: number;
	    Saturation: number;
	    Dither: string;
	    NoMerge: boolean;
	    NoSimplify: boolean;
	    Size?: number;
	    Force: boolean;
	    Stats: boolean;
	    ColorSnap: number;
	
	    static createFrom(source: any = {}) {
	        return new Options(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Input = source["Input"];
	        this.NumColors = source["NumColors"];
	        this.LockedColors = source["LockedColors"];
	        this.AutoColors = source["AutoColors"];
	        this.Scale = source["Scale"];
	        this.Output = source["Output"];
	        this.NozzleDiameter = source["NozzleDiameter"];
	        this.LayerHeight = source["LayerHeight"];
	        this.InventoryFile = source["InventoryFile"];
	        this.InventoryColors = source["InventoryColors"];
	        this.InventoryLabels = source["InventoryLabels"];
	        this.Brightness = source["Brightness"];
	        this.Contrast = source["Contrast"];
	        this.Saturation = source["Saturation"];
	        this.Dither = source["Dither"];
	        this.NoMerge = source["NoMerge"];
	        this.NoSimplify = source["NoSimplify"];
	        this.Size = source["Size"];
	        this.Force = source["Force"];
	        this.Stats = source["Stats"];
	        this.ColorSnap = source["ColorSnap"];
	    }
	}

}

