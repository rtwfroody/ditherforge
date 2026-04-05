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
	    Palette: string;
	    AutoPalette?: number;
	    Scale: number;
	    Output: string;
	    NozzleDiameter: number;
	    LayerHeight: number;
	    InventoryFile: string;
	    Inventory?: number;
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
	        this.Palette = source["Palette"];
	        this.AutoPalette = source["AutoPalette"];
	        this.Scale = source["Scale"];
	        this.Output = source["Output"];
	        this.NozzleDiameter = source["NozzleDiameter"];
	        this.LayerHeight = source["LayerHeight"];
	        this.InventoryFile = source["InventoryFile"];
	        this.Inventory = source["Inventory"];
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
	    InputMesh?: MeshData;
	    OutputMesh?: MeshData;
	    Duration: number;
	
	    static createFrom(source: any = {}) {
	        return new ProcessResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.NeedsForce = source["NeedsForce"];
	        this.ModelExtentMM = source["ModelExtentMM"];
	        this.InputMesh = this.convertValues(source["InputMesh"], MeshData);
	        this.OutputMesh = this.convertValues(source["OutputMesh"], MeshData);
	        this.Duration = source["Duration"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

