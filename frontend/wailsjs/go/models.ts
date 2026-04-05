export namespace pipeline {
	
	export class MeshData {
	    Vertices: number[];
	    Faces: number[];
	    FaceColors: number[];
	    UVs?: number[];
	    Textures?: string[];
	    FaceTextureIdx?: number[];
	
	    static createFrom(source: any = {}) {
	        return new MeshData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Vertices = source["Vertices"];
	        this.Faces = source["Faces"];
	        this.FaceColors = source["FaceColors"];
	        this.UVs = source["UVs"];
	        this.Textures = source["Textures"];
	        this.FaceTextureIdx = source["FaceTextureIdx"];
	    }
	}
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
	export class ProcessResult {
	    NeedsForce: boolean;
	    ModelExtentMM: number;
	    Duration: number;
	
	    static createFrom(source: any = {}) {
	        return new ProcessResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.NeedsForce = source["NeedsForce"];
	        this.ModelExtentMM = source["ModelExtentMM"];
	        this.Duration = source["Duration"];
	    }
	}

}

